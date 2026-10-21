package auth

import (
	"fmt"
	"regexp"
	"sort"
)

// authorizerIDPattern 约束模块路由取用的 authorizer ID。
//
// 该 ID 会进日志与 metrics label，必须低基数、稳定、不含用户输入。
var authorizerIDPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,127}$`)

// AuthorizerRegistry 持有具名 authorizer，在装配期填充，此后只读。
//
// 未注册标识的处置收口在装配层：按 ID 取用时兜底为恒拒绝并告警
// （"可启动且不放行"，见 bootstrap 的 newAuthorizeByID），因此注册表
// 只提供注册与查找两个入口。写入只发生在装配的单线程窗口内，
// 装配完成后不存在任何写路径。
type AuthorizerRegistry struct {
	entries map[string]Authorizer
}

func NewAuthorizerRegistry() *AuthorizerRegistry {
	return &AuthorizerRegistry{entries: make(map[string]Authorizer)}
}

// Register 登记一个具名 authorizer。
//
// 重复 ID 直接报错而不是覆盖：覆盖会让"最后一个注册的赢"，
// 而注册顺序取决于装配代码的书写顺序，极难察觉。
func (r *AuthorizerRegistry) Register(id string, a Authorizer) error {
	if !authorizerIDPattern.MatchString(id) {
		return fmt.Errorf("authorizer id %q must match %s", id, authorizerIDPattern.String())
	}
	if a == nil {
		return fmt.Errorf("authorizer %q must not be nil", id)
	}
	if _, dup := r.entries[id]; dup {
		return fmt.Errorf("authorizer %q is already registered", id)
	}
	r.entries[id] = a
	return nil
}

// Lookup 按 ID 取 authorizer。
func (r *AuthorizerRegistry) Lookup(id string) (Authorizer, bool) {
	if r == nil {
		return nil, false
	}
	a, ok := r.entries[id]
	return a, ok
}

// IDs 返回已登记的全部 ID，按字典序——错误信息里需要稳定顺序。
func (r *AuthorizerRegistry) IDs() []string {
	if r == nil {
		return nil
	}
	ids := make([]string, 0, len(r.entries))
	for id := range r.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
