package routespec

import (
	"net/http"
	"slices"
	"strings"
)

// allowHeaderOrder 是 Allow 头里方法的固定输出顺序。
//
// 固定顺序而不是依赖 map 遍历或 Gin 内部 method tree 的顺序：
// 响应头是对外契约，顺序抖动会让缓存、快照测试与告警去重都不稳定。
var allowHeaderOrder = []string{
	http.MethodGet, http.MethodHead, http.MethodPost,
	http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions,
}

// RouteIndex 是编译完成后冻结的路径 → 方法集合索引。
//
// 用途是 405 的 Allow 头与 CORS 预检的方法推导。它在 compile 之后构建、随后只读，
// 运行期不允许再注册或替换——路由集合是启动期事实，运行期可变会让"实际路由"与
// "契约"重新分叉。
//
// 不复用 Gin 内部的 method tree：当前锁定版本在 HandleMethodNotAllowed 下确实会写
// Allow，但其顺序来自内部实现细节、不是本项目的稳定契约，且不会包含由 CORS 派生的 OPTIONS。
type RouteIndex struct {
	// static 是无参数段路径的精确索引。
	static map[string][]string
	// patterns 保留带 :param / *wildcard 的路径，按 Gin 的匹配语义逐段比较。
	patterns []indexedPattern
}

type indexedPattern struct {
	segments []string
	methods  []string
	// catchAll 表示模式以 *wildcard 结尾，可匹配任意剩余段。
	catchAll bool
}

// BuildIndex 由编译结果构建不可变索引。
func BuildIndex(routes []CompiledRoute) *RouteIndex {
	byPath := make(map[string][]string, len(routes))
	for _, r := range routes {
		byPath[r.FullPath] = appendMethod(byPath[r.FullPath], r.Spec.Method)
	}

	idx := &RouteIndex{static: make(map[string][]string)}
	for path, methods := range byPath {
		methods = sortMethods(methods)
		if !strings.ContainsAny(path, ":*") {
			idx.static[path] = methods
			continue
		}
		segs := splitPath(path)
		idx.patterns = append(idx.patterns, indexedPattern{
			segments: segs,
			methods:  methods,
			catchAll: len(segs) > 0 && strings.HasPrefix(segs[len(segs)-1], "*"),
		})
	}
	return idx
}

// AllowedMethods 返回该路径已注册的方法集合（含派生的 HEAD/OPTIONS 规则由调用方决定）。
//
// 返回 nil 表示该路径根本不存在——调用方据此区分 404 与 405：
// 路径不存在是 404，路径存在但方法不对才是 405。把两者混淆会把探测未知路径的
// 扫描流量误报成 405，反而泄漏"哪些路径存在"。
func (idx *RouteIndex) AllowedMethods(path string) []string {
	if idx == nil {
		return nil
	}
	if methods, ok := idx.static[path]; ok {
		return slices.Clone(methods)
	}
	// 静态优先：只有静态表没命中才回退到模式匹配，
	// 否则 /user/me 会被 /user/:id 抢走。
	segs := splitPath(path)
	for _, p := range idx.patterns {
		if p.matches(segs) {
			return slices.Clone(p.methods)
		}
	}
	return nil
}

func (p indexedPattern) matches(segs []string) bool {
	if p.catchAll {
		// *wildcard 吃掉剩余全部段，因此只要求前缀段数足够。
		if len(segs) < len(p.segments)-1 {
			return false
		}
	} else if len(segs) != len(p.segments) {
		return false
	}

	for i, want := range p.segments {
		if strings.HasPrefix(want, "*") {
			return true // catch-all，剩余段一律匹配。
		}
		if i >= len(segs) {
			return false
		}
		if strings.HasPrefix(want, ":") {
			// 参数段可以匹配任意**非空**段。
			if segs[i] == "" {
				return false
			}
			continue
		}
		if want != segs[i] {
			return false
		}
	}
	return true
}

func splitPath(p string) []string {
	trimmed := strings.Trim(p, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func appendMethod(methods []string, m string) []string {
	if slices.Contains(methods, m) {
		return methods
	}
	return append(methods, m)
}

// sortMethods 按固定顺序排列方法，未知方法排在末尾并保持字典序。
func sortMethods(methods []string) []string {
	out := slices.Clone(methods)
	slices.SortFunc(out, func(a, b string) int {
		ia, ib := slices.Index(allowHeaderOrder, a), slices.Index(allowHeaderOrder, b)
		switch {
		case ia >= 0 && ib >= 0:
			return ia - ib
		case ia >= 0:
			return -1
		case ib >= 0:
			return 1
		default:
			return strings.Compare(a, b)
		}
	})
	return out
}

// AllowHeaderValue 生成 405 响应的 Allow 头值。
//
// 自动补 HEAD：注册了 GET 的路径按 HTTP 语义同样接受 HEAD。
// 自动补 OPTIONS：预检由 CORS 派生，客户端确实可以对该路径发 OPTIONS。
// 这两项都不在模块声明里，但它们是真实可用的方法，不列出会让客户端误判。
func (idx *RouteIndex) AllowHeaderValue(path string) string {
	methods := idx.AllowedMethods(path)
	if len(methods) == 0 {
		return ""
	}
	if slices.Contains(methods, http.MethodGet) {
		methods = appendMethod(methods, http.MethodHead)
	}
	methods = appendMethod(methods, http.MethodOptions)
	return strings.Join(sortMethods(methods), ", ")
}
