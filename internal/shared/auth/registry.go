package auth

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
)

// authorizerIDPattern 与路由描述符里的 authorizer ID 同源。
//
// 该 ID 会进日志与 metrics label，必须低基数、稳定、不含用户输入。
var authorizerIDPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,127}$`)

// AuthorizerRegistry 持有具名 authorizer，在装配期填充、编译前冻结。
//
// 冻结是硬约束：路由集合与授权实现都是启动期事实。允许运行期注册或替换，
// 就会出现"某条路由的授权实现中途换了"这种无法复现的行为，
// 也让"启动即校验全部引用"失去意义。
type AuthorizerRegistry struct {
	entries map[string]Authorizer
	frozen  bool
}

func NewAuthorizerRegistry() *AuthorizerRegistry {
	return &AuthorizerRegistry{entries: make(map[string]Authorizer)}
}

// Register 登记一个具名 authorizer。
//
// 重复 ID 直接报错而不是覆盖：覆盖会让"最后一个注册的赢"，
// 而注册顺序取决于装配代码的书写顺序，极难察觉。
func (r *AuthorizerRegistry) Register(id string, a Authorizer) error {
	if r.frozen {
		return fmt.Errorf("authorizer registry is frozen; %q cannot be registered after compilation", id)
	}
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

// Freeze 封闭注册表，此后只读。
func (r *AuthorizerRegistry) Freeze() { r.frozen = true }

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

// ResolveAll 校验一批 authorizer 引用能否全部解析。
//
// 启动期一次性校验，而不是等请求到来才发现 ID 写错——后者会让一条路由
// 在上线后才暴露"授权实现不存在"，而那时的安全行为是未定义的。
func (r *AuthorizerRegistry) ResolveAll(ids []string) error {
	var errs []error
	for _, id := range ids {
		if _, ok := r.Lookup(id); !ok {
			errs = append(errs, fmt.Errorf(
				"authorizer %q is referenced by a route but not registered (registered: %v)", id, r.IDs()))
		}
	}
	return errors.Join(errs...)
}
