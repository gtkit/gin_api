package bootstrap

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gtkit/gin-api/internal/module/auth/dto"
	"github.com/gtkit/gin-api/internal/pkg/credential"
	"github.com/gtkit/gin-api/internal/platform/config"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type changedPasswordHash struct {
	hasher *credential.Hasher
	value  *string
}

func TestLogoutKeepsOtherAccountSignedIn(t *testing.T) {
	for _, route := range []string{"logout", "logout-all"} {
		t.Run(route, func(t *testing.T) { checkLogoutPreservesOtherAccount(t, route) })
	}
}

func checkLogoutPreservesOtherAccount(t *testing.T, route string) {
	t.Helper()
	conn, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for range conn.Stats().OpenConnections {
			mock.ExpectClose()
		}
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: conn, SkipInitializeWithVersion: true}),
		&gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	hasher, err := credential.NewHasher(10)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := hasher.Hash("fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	engine := openAPIEngineWithDB(t, config.AuthModeLocal, true, db)
	send := func(route, body, access string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/"+route, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if access != "" {
			req.Header.Set("Authorization", "Bearer "+access)
		}
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		return rec
	}
	request := func(route, body, access string, want int) *httptest.ResponseRecorder {
		t.Helper()
		rec := send(route, body, access)
		if rec.Code != want {
			t.Fatalf("%s HTTP %d, want %d", route, rec.Code, want)
		}
		return rec
	}
	login := func(name string, uid int64) dto.TokenResponse {
		t.Helper()
		mock.ExpectQuery("SELECT `id`,`password`,`status`,`auth_version` FROM `users`").WithArgs(name, 1).
			WillReturnRows(sqlmock.NewRows([]string{"id", "password", "status", "auth_version"}).AddRow(uid, hash, 1, 0))
		rec := request("login", `{"phone":"`+name+`","password":"fixture-password"}`, "", http.StatusOK)
		var envelope struct{ Data dto.TokenResponse }
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Data.AccessToken == "" || envelope.Data.RefreshToken == "" {
			t.Fatal("login returned an incomplete token pair")
		}
		return envelope.Data
	}
	expectSession := func(uid int64, live bool) {
		t.Helper()
		rows := sqlmock.NewRows([]string{"id"})
		if live {
			rows.AddRow(uid)
		}
		mock.ExpectQuery("SELECT `id` FROM `users`").WithArgs(uid, 1, 0, 1).
			WillReturnRows(rows)
	}
	first, second := login("fixture-a", 7), login("fixture-b", 8)
	ownerBody := `{"refresh_token":"` + second.RefreshToken + `"}`
	mock.MatchExpectationsInOrder(false)
	expectSession(7, true)
	expectSession(8, true)
	logoutBody := ownerBody
	if route == "logout-all" {
		logoutBody = ""
		mock.ExpectBegin()
		mock.ExpectExec("UPDATE `users` SET `auth_version`=auth_version \\+ 1,.*WHERE.*id = \\? AND status = \\? AND auth_version = \\?").
			WithArgs(sqlmock.AnyArg(), 7, 1, 0).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
	}
	var logout, refresh *httptest.ResponseRecorder
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Go(func() {
		<-start
		logout = send(route, logoutBody, first.AccessToken)
	})
	wg.Go(func() {
		<-start
		refresh = send("refresh", ownerBody, "")
	})
	close(start)
	wg.Wait()
	mock.MatchExpectationsInOrder(true)
	if logout.Code != http.StatusOK || refresh.Code != http.StatusOK {
		t.Fatalf("concurrent logout of A=%d, refresh of B=%d", logout.Code, refresh.Code)
	}
	var successor struct{ Data dto.TokenResponse }
	if err := json.Unmarshal(refresh.Body.Bytes(), &successor); err != nil || successor.Data.RefreshToken == "" {
		t.Fatalf("decode B's rotated token: %v", err)
	}
	expectSession(7, route == "logout")
	request("logout", `{}`, first.AccessToken, http.StatusUnauthorized)
	if route == "logout-all" {
		expectSession(7, false)
		request("refresh", `{"refresh_token":"`+first.RefreshToken+`"}`, "", http.StatusUnauthorized)
	}
	expectSession(8, true)
	request("refresh", ownerBody, "", http.StatusOK)
	expectSession(8, true)
	request("logout", ownerBody, second.AccessToken, http.StatusOK)
	request("refresh", `{"refresh_token":"`+successor.Data.RefreshToken+`"}`, "", http.StatusUnauthorized)
}

func (m changedPasswordHash) Match(value driver.Value) bool {
	hash, ok := value.(string)
	if !ok || m.hasher.Compare(hash, "new-password") != nil {
		return false
	}
	*m.value = hash
	return true
}

// Uses real routing, runtime adapters, bcrypt, JWT and Redis Lua. SQL is mocked;
// database concurrency and migration execution require a separate database run.
func TestSessionLifecycleHTTP(t *testing.T) {
	conn, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		mock.ExpectClose()
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: conn, SkipInitializeWithVersion: true}),
		&gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	hasher, err := credential.NewHasher(10)
	if err != nil {
		t.Fatal(err)
	}
	oldHash, err := hasher.Hash("old-password")
	if err != nil {
		t.Fatal(err)
	}
	engine := openAPIEngineWithDB(t, config.AuthModeLocal, true, db)
	request := func(path, body, token string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("%s status=%d want=%d", path, rec.Code, status)
		}
		if strings.Contains(rec.Body.String(), "private database error") {
			t.Fatal("database detail leaked")
		}
		if status == 200 && rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("authentication response is cacheable")
		}
		return rec
	}
	credentialRead := func(hash string, version int64, byPhone bool) {
		t.Helper()
		query := mock.ExpectQuery("SELECT `id`,`password`,`status`,`auth_version` FROM `users`")
		if byPhone {
			query.WithArgs("account", 1)
		} else {
			query.WithArgs(7, 1)
		}
		query.WillReturnRows(sqlmock.NewRows([]string{"id", "password", "status", "auth_version"}).AddRow(7, hash, 1, version))
	}
	sessionRead := func(version, matches int64) {
		t.Helper()
		rows := sqlmock.NewRows([]string{"id"})
		if matches > 0 {
			rows.AddRow(7)
		}
		mock.ExpectQuery("SELECT `id` FROM `users` WHERE \\(id = \\? AND status = \\? AND auth_version = \\?\\) AND `users`.`deleted_at` IS NULL LIMIT \\?").
			WithArgs(7, 1, version, 1).WillReturnRows(rows)
	}
	decodeToken := func(response *httptest.ResponseRecorder) dto.TokenResponse {
		t.Helper()
		var envelope struct{ Data dto.TokenResponse }
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Data.AccessToken == "" || envelope.Data.RefreshToken == "" {
			t.Fatal("missing token pair")
		}
		return envelope.Data
	}

	credentialRead(oldHash, 0, true)
	old := decodeToken(request("login", `{"phone":"account","password":"old-password"}`, "", 200))
	request("logout-all", `{"user_id":99}`, old.AccessToken, 400)
	request("password/change", `{"old_password":"old-password","new_password":"new-password"}`, "", 401)
	sessionRead(0, 1)
	request("password/change", `{"old_password":"old-password","new_password":"short"}`, old.AccessToken, 422)
	sessionRead(0, 1)
	credentialRead(oldHash, 0, false)
	request("password/change", `{"old_password":"wrong-password","new_password":"new-password"}`, old.AccessToken, 401)

	sessionRead(0, 1)
	credentialRead(oldHash, 0, false)
	mock.ExpectBegin()
	newHash := ""
	mock.ExpectExec("UPDATE `users` SET `auth_version`=auth_version \\+ 1,`password`=\\?,`updated_at`=\\? WHERE \\(id = \\? AND status = \\? AND auth_version = \\? AND password = \\?\\) AND `users`.`deleted_at` IS NULL").
		WithArgs(changedPasswordHash{hasher: hasher, value: &newHash}, sqlmock.AnyArg(), 7, 1, 0, oldHash).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	response := request("password/change", `{"old_password":"old-password","new_password":"new-password"}`, old.AccessToken, 200)
	if strings.Contains(response.Body.String(), "access_token") || newHash == "" {
		t.Fatal("password change must persist a hash and require a fresh login")
	}
	sessionRead(0, 0)
	request("logout-all", "", old.AccessToken, 401)
	sessionRead(0, 0)
	request("refresh", `{"refresh_token":"`+old.RefreshToken+`"}`, "", 401)
	credentialRead(newHash, 1, true)
	current := decodeToken(request("login", `{"phone":"account","password":"new-password"}`, "", 200))

	mock.ExpectQuery("SELECT `id` FROM `users`").WithArgs(7, 1, 1, 1).
		WillReturnError(errors.New("private database error"))
	refreshBody := `{"refresh_token":"` + current.RefreshToken + `"}`
	request("refresh", refreshBody, "", 500)
	sessionRead(1, 1)
	current = decodeToken(request("refresh", refreshBody, "", 200))

	// A database outage must not be presented as an expired credential.
	mock.ExpectQuery("SELECT `id` FROM `users`").WillReturnError(errors.New("private database error"))
	request("logout-all", "", current.AccessToken, 503)
	sessionRead(1, 1)
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `users` SET `auth_version`=auth_version \\+ 1,`updated_at`=\\? WHERE \\(id = \\? AND status = \\? AND auth_version = \\?\\) AND `users`.`deleted_at` IS NULL").
		WithArgs(sqlmock.AnyArg(), 7, 1, 1).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	request("logout-all", "", current.AccessToken, 200)
	sessionRead(1, 0)
	request("logout-all", "", current.AccessToken, 401)
	sessionRead(1, 0)
	request("refresh", `{"refresh_token":"`+current.RefreshToken+`"}`, "", 401)
}
