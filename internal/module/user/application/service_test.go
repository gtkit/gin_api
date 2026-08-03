package application

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/models"

	"gorm.io/gorm/schema"
)

type fakeUserRepo struct {
	exists    bool
	existsErr error
	user      models.User
	getErr    error

	// Update 记录
	updUserID int64
	updFields map[string]any
	updErr    error

	// 注销流程控制
	lockUser     *models.User
	lockFound    bool
	lockErr      error
	anonCalls    int
	anonPhone    string
	softDelCalls int
}

func (f *fakeUserRepo) ExistsByID(context.Context, int64) (bool, error) {
	return f.exists, f.existsErr
}
func (f *fakeUserRepo) Get(context.Context, string) (models.User, bool, error) {
	return f.user, f.user.ID != 0, f.getErr
}
func (f *fakeUserRepo) Update(_ context.Context, userID int64, fields map[string]any) error {
	f.updUserID = userID
	f.updFields = fields
	return f.updErr
}
func (f *fakeUserRepo) LockByID(context.Context, int64) (*models.User, bool, error) {
	return f.lockUser, f.lockFound, f.lockErr
}
func (f *fakeUserRepo) AnonymizeForDeactivation(_ context.Context, _ int64, phone, _ string) error {
	f.anonCalls++
	f.anonPhone = phone
	return nil
}
func (f *fakeUserRepo) SoftDeleteProfile(context.Context, int64) error {
	f.softDelCalls++
	return nil
}

// newTestService 构造依赖齐全的 Service。
//
// 必需依赖缺一即装配失败，所以用例除了自己关心的 repo 之外，
// 其余依赖一律用不产生副作用的 fake 填满，而不是留 nil。
func newTestService(t *testing.T, repo UserRepository, opts ...ServiceOption) *Service {
	t.Helper()

	svc, err := NewService(Deps{
		Repo:     repo,
		Recorder: &fakeDeactRecorder{},
		Tx:       &fakeTxManager{},
	}, opts...)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// TestNewServiceRejectsIncompleteDeps 是装配期 fail-closed 的正向证明：
// 任一必需依赖缺失都必须在构造时报错，且错误信息点名缺了哪个字段。
//
// 没有这条，"依赖不全"会重新退化成运行期 nil panic 或某个含义不明的业务错误码。
func TestNewServiceRejectsIncompleteDeps(t *testing.T) {
	t.Parallel()

	full := Deps{Repo: &fakeUserRepo{}, Recorder: &fakeDeactRecorder{}, Tx: &fakeTxManager{}}

	cases := []struct {
		name     string
		mutate   func(*Deps)
		wantErr  bool
		wantName string
	}{
		{name: "complete deps", mutate: func(*Deps) {}, wantErr: false},
		{name: "missing repo", mutate: func(d *Deps) { d.Repo = nil }, wantErr: true, wantName: "Repo"},
		{name: "missing recorder", mutate: func(d *Deps) { d.Recorder = nil }, wantErr: true, wantName: "Recorder"},
		{name: "missing tx", mutate: func(d *Deps) { d.Tx = nil }, wantErr: true, wantName: "Tx"},
		{name: "zero deps", mutate: func(d *Deps) { *d = Deps{} }, wantErr: true, wantName: "Repo, Recorder, Tx"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			deps := full
			tc.mutate(&deps)

			svc, err := NewService(deps)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("NewService() error = %v, want nil", err)
				}
				if svc == nil {
					t.Fatal("NewService() returned nil service without error")
				}
				return
			}

			if !errors.Is(err, ErrIncompleteDeps) {
				t.Fatalf("NewService() error = %v, want ErrIncompleteDeps", err)
			}
			if svc != nil {
				t.Fatal("NewService() returned a service alongside an error; assembly must fail closed")
			}
			if !strings.Contains(err.Error(), tc.wantName) {
				t.Fatalf("error %q does not name the missing dependency %q", err, tc.wantName)
			}
		})
	}
}

func TestValidateUser(t *testing.T) {
	t.Parallel()

	service := newTestService(t, &fakeUserRepo{exists: true})
	ok, err := service.ValidateUser(context.Background(), 42)
	if err != nil {
		t.Fatalf("ValidateUser returned error: %v", err)
	}
	if !ok {
		t.Fatal("ValidateUser returned false, want true")
	}
}

// TestValidateUserSurfacesRepoError 验证仓储访问失败时 ValidateUser 上抛 error，
// 而不是把库故障静默降级成"用户无效"——上层据此返回 503 而非 401。
func TestValidateUserSurfacesRepoError(t *testing.T) {
	t.Parallel()

	service := newTestService(t, &fakeUserRepo{exists: true, existsErr: errors.New("db down")})
	ok, err := service.ValidateUser(context.Background(), 42)
	if err == nil {
		t.Fatal("ValidateUser error = nil on repo failure, want error surfaced")
	}
	if ok {
		t.Fatal("ValidateUser returned true on repo error, want false")
	}
}

func TestValidateVIP(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		user models.User
		want bool
	}{
		{
			name: "active vip without expire time",
			user: models.User{
				BaseID:    models.BaseID{ID: 42},
				VipStatus: models.VipStatusActive,
			},
			want: true,
		},
		{
			name: "active vip with future expire time",
			user: models.User{
				BaseID:      models.BaseID{ID: 42},
				VipStatus:   models.VipStatusActive,
				VipExpireAt: timePtr(time.Now().Add(time.Hour)),
			},
			want: true,
		},
		{
			name: "inactive vip",
			user: models.User{
				BaseID:    models.BaseID{ID: 42},
				VipStatus: models.VipStatusInactive,
			},
			want: false,
		},
		{
			name: "expired vip",
			user: models.User{
				BaseID:      models.BaseID{ID: 42},
				VipStatus:   models.VipStatusActive,
				VipExpireAt: timePtr(time.Now().Add(-time.Hour)),
			},
			want: false,
		},
		{
			name: "missing user",
			user: models.User{},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			service := newTestService(t, &fakeUserRepo{user: tc.user})
			got, err := service.ValidateVIP(context.Background(), 42)
			if err != nil {
				t.Fatalf("ValidateVIP() error = %v, want nil", err)
			}
			if got != tc.want {
				t.Fatalf("ValidateVIP() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestValidateVIPExpiryBoundaryWithInjectedClock 注入固定时钟，确定性地覆盖
// "恰好在过期时刻"这一边界——用 time.Now() 无法稳定测到的场景。
// 规则：VipExpireAt 必须严格晚于当前时刻才算有效（!After(now) 即过期）。
func TestValidateVIPExpiryBoundaryWithInjectedClock(t *testing.T) {
	t.Parallel()

	fixed := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		expire time.Time
		want   bool
	}{
		{name: "expire exactly at now is expired", expire: fixed, want: false},
		{name: "expire one nanosecond after now is active", expire: fixed.Add(time.Nanosecond), want: true},
		{name: "expire one nanosecond before now is expired", expire: fixed.Add(-time.Nanosecond), want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			service := newTestService(t, &fakeUserRepo{user: models.User{
				BaseID:      models.BaseID{ID: 42},
				VipStatus:   models.VipStatusActive,
				VipExpireAt: timePtr(tc.expire),
			}})
			service.now = func() time.Time { return fixed } // 白盒注入固定时钟

			got, err := service.ValidateVIP(context.Background(), 42)
			if err != nil {
				t.Fatalf("ValidateVIP() error = %v, want nil", err)
			}
			if got != tc.want {
				t.Fatalf("ValidateVIP() at boundary = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestValidateVIPSurfacesRepoError 验证仓储故障上抛 error，而不是被折叠成
// "不是会员"。折叠会让一次数据库抖动表现为全体会员静默失去权益——
// 客户端看到的是一个正常的业务拒绝，既不重试也不触发告警。
func TestValidateVIPSurfacesRepoError(t *testing.T) {
	t.Parallel()

	service := newTestService(t, &fakeUserRepo{
		user:   models.User{BaseID: models.BaseID{ID: 42}, VipStatus: models.VipStatusActive},
		getErr: errors.New("db down"),
	})

	ok, err := service.ValidateVIP(context.Background(), 42)
	if err == nil {
		t.Fatal("ValidateVIP() error = nil on repo failure, want error surfaced")
	}
	if ok {
		t.Fatal("ValidateVIP() = true on repo error, want false")
	}
}

func TestProfileMapsUser(t *testing.T) {
	t.Parallel()

	service := newTestService(t, &fakeUserRepo{
		user: models.User{
			BaseID:    models.BaseID{ID: 9},
			Nickname:  "u1",
			Gender:    models.UserGenderMale,
			Avatar:    "avatar",
			Phone:     "13800138000",
			Status:    models.UserStatusNormal,
			VipStatus: models.VipStatusActive,
			VipLevel:  2,
		},
	})

	result, err := service.Profile(context.Background(), 9)
	if err != nil {
		t.Fatalf("Profile returned error: %v", err)
	}
	if result.ID != 9 || result.Nickname != "u1" {
		t.Fatalf("Profile = %+v", result)
	}
}

func timePtr(t time.Time) *time.Time {
	return &t
}

// TestUpdateProfilePartial 验证 partial update：只更新显式给出的字段，
// 且零值（gender=0）能被写入，未传字段（avatar）不进更新集。
func TestUpdateProfilePartial(t *testing.T) {
	t.Parallel()

	repo := &fakeUserRepo{}
	service := newTestService(t, repo)

	err := service.UpdateProfile(context.Background(), 7, UpdateProfileCommand{
		NickName: "newnick",
		Gender:   new(int8(0)),
	})
	if err != nil {
		t.Fatalf("UpdateProfile returned error: %v", err)
	}
	if repo.updUserID != 7 {
		t.Fatalf("updUserID = %d, want 7", repo.updUserID)
	}
	if len(repo.updFields) != 2 {
		t.Fatalf("updFields = %+v, want 2 keys (nickname, gender)", repo.updFields)
	}
	// 列名必须是 nickname（models.User 的 gorm column），不是 nick_name。
	if repo.updFields["nickname"] != "newnick" {
		t.Fatalf("nickname = %v, want newnick", repo.updFields["nickname"])
	}
	if repo.updFields["gender"] != models.UserGender(0) {
		t.Fatalf("gender = %v, want UserGender(0)", repo.updFields["gender"])
	}
	if _, ok := repo.updFields["avatar"]; ok {
		t.Fatal("avatar should not be in update fields when not provided")
	}
}

// TestUpdateProfileEmitsValidColumns 是列名漂移的回归守卫：
// UpdateProfile 产出的每个更新 map key 必须是 models.User 的真实 gorm 列名。
// 用 schema 解析代替真实库——DryRun 方言不校验列存在，只有 schema 一致性能抓住
// nick_name 这类"语法合法但列不存在"的 bug。
func TestUpdateProfileEmitsValidColumns(t *testing.T) {
	t.Parallel()

	repo := &fakeUserRepo{}
	service := newTestService(t, repo)
	gender := int8(1)
	if err := service.UpdateProfile(context.Background(), 7, UpdateProfileCommand{
		NickName: "n",
		Gender:   &gender,
		Avatar:   "http://example.com/a.png",
	}); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if len(repo.updFields) == 0 {
		t.Fatal("expected update fields to be captured")
	}

	s, err := schema.Parse(&models.User{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("parse user schema: %v", err)
	}

	for key := range repo.updFields {
		if _, ok := s.FieldsByDBName[key]; !ok {
			t.Errorf("update column key %q is not a real models.User column", key)
		}
	}
}

// TestUpdateProfileNoFields 验证全部未传时不触库。
func TestUpdateProfileNoFields(t *testing.T) {
	t.Parallel()

	repo := &fakeUserRepo{}
	service := newTestService(t, repo)

	if err := service.UpdateProfile(context.Background(), 7, UpdateProfileCommand{}); err != nil {
		t.Fatalf("UpdateProfile returned error: %v", err)
	}
	if repo.updFields != nil {
		t.Fatalf("expected no repo update, got fields = %+v", repo.updFields)
	}
}
