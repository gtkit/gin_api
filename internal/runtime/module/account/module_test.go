package account

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gtkit/gin-api/internal/models"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestNewRejectsMissingDatabase(t *testing.T) {
	t.Parallel()
	assembly, err := New(nil)
	if err == nil || assembly.Users != nil || assembly.VIP != nil || assembly.Roles != nil {
		t.Fatalf("missing database: assembly=%+v err=%v", assembly, err)
	}
}

// An uninitialized GORM value has no live connection. Construction must only
// wire the three ports; a database operation here would panic or return an error.
func TestNewWiresAllIdentityPortsWithoutDatabaseIO(t *testing.T) {
	t.Parallel()
	assembly, err := New(&gorm.DB{})
	if err != nil {
		t.Fatal(err)
	}
	if assembly.Users == nil || assembly.VIP == nil || assembly.Roles == nil {
		t.Fatalf("missing identity port: %+v", assembly)
	}
	if any(assembly.Users) != any(assembly.VIP) {
		t.Fatal("user and VIP verification must share one account repository")
	}
}

// The SQL double supplies repository results through the actual assembled
// ports. These cases verify delegation and error/clock semantics, not a real
// database schema or the optimizer's handling of these queries.
func TestAssembledPortsPreserveIdentityDecisionsAndCauses(t *testing.T) {
	assembly, mock := newTestAssembly(t)
	failure := errors.New("identity database unavailable")
	for _, tc := range []struct {
		name  string
		count int64
		err   error
	}{
		{name: "enabled account", count: 1},
		{name: "disabled or missing account"},
		{name: "query failure", err: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query := mock.ExpectQuery("SELECT count.*FROM `users`").WithArgs(int64(7), models.UserStatusNormal, 1)
			if tc.err != nil {
				query.WillReturnError(tc.err)
			} else {
				query.WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(tc.count))
			}
			allowed, err := assembly.Users.ValidateUser(t.Context(), 7)
			if allowed != (tc.count > 0) || !errors.Is(err, tc.err) {
				t.Fatalf("account decision=%v err=%v", allowed, err)
			}
		})
	}
	for _, tc := range []struct {
		name  string
		count int64
		err   error
	}{
		{name: "role granted", count: 1},
		{name: "role denied"},
		{name: "role source failed", err: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query := mock.ExpectQuery("SELECT count.*FROM `user_roles`").WithArgs(int64(7), "admin", 1)
			if tc.err != nil {
				query.WillReturnError(tc.err)
			} else {
				query.WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(tc.count))
			}
			allowed, err := assembly.Roles.HasRole(t.Context(), 7, "admin")
			if allowed != (tc.count > 0) || !errors.Is(err, tc.err) {
				t.Fatalf("role decision=%v err=%v", allowed, err)
			}
		})
	}
	for _, verify := range []func(context.Context) (bool, error){
		func(ctx context.Context) (bool, error) { return assembly.Users.ValidateUser(ctx, 0) },
		func(ctx context.Context) (bool, error) { return assembly.VIP.ValidateVIP(ctx, -1) },
		func(ctx context.Context) (bool, error) { return assembly.Roles.HasRole(ctx, 7, "") },
	} {
		if allowed, err := verify(t.Context()); allowed || err != nil {
			t.Fatalf("invalid identity must be denied without I/O: allowed=%v err=%v", allowed, err)
		}
	}
}

func TestAssembledVIPUsesTheCurrentClockAndPreservesFailure(t *testing.T) {
	assembly, mock := newTestAssembly(t)
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		past, future := now.Add(-time.Second), now.Add(time.Second)
		failure := errors.New("VIP source unavailable")
		for _, tc := range []struct {
			name   string
			id     int64
			status models.VipStatus
			expiry *time.Time
			allow  bool
			err    error
		}{
			{name: "no expiry", id: 7, status: models.VipStatusActive, allow: true},
			{name: "future expiry", id: 7, status: models.VipStatusActive, expiry: &future, allow: true},
			{name: "expires now", id: 7, status: models.VipStatusActive, expiry: &now},
			{name: "already expired", id: 7, status: models.VipStatusActive, expiry: &past},
			{name: "inactive", id: 7, status: models.VipStatusInactive},
			{name: "disabled or absent"},
			{name: "query failure", err: failure},
		} {
			query := mock.ExpectQuery("SELECT .*FROM `users`").WithArgs(int64(7), models.UserStatusNormal, 1)
			if tc.err != nil {
				query.WillReturnError(tc.err)
			} else {
				rows := sqlmock.NewRows([]string{"id", "status", "vip_status", "vip_expire_at"})
				if tc.id != 0 {
					rows.AddRow(tc.id, models.UserStatusNormal, tc.status, tc.expiry)
				}
				query.WillReturnRows(rows)
			}
			allowed, err := assembly.VIP.ValidateVIP(t.Context(), 7)
			if allowed != tc.allow || !errors.Is(err, tc.err) {
				t.Fatalf("%s: decision=%v err=%v", tc.name, allowed, err)
			}
		}
	})
}

func newTestAssembly(t *testing.T) (Assembly, sqlmock.Sqlmock) {
	t.Helper()
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		mock.ExpectClose()
		if err := database.Close(); err != nil {
			t.Error(err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: database, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	assembly, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	return assembly, mock
}
