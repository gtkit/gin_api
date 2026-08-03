package routespec

import (
	"fmt"

	"github.com/gin-gonic/gin"
)

// detectPathTreeConflicts 在**一次性的 throwaway engine** 上预演注册，
// 把 Gin 的路径树冲突从 panic 转成启动错误。
//
// 为什么需要这一步：像 /user/:id 与 /user/:name 这样的冲突只有 Gin 的路由树知道
// （两者路径字符串不同，前面的重复检查抓不到），而 Gin 在注册冲突时**直接 panic**。
// 若直接往生产 engine 上注册，进程会崩，且此时 engine 已被写入一半。
//
// throwaway engine 用完即弃，绝不复用为生产 engine——预演的目的就是让失败发生在
// 一个可丢弃的对象上。
func detectPathTreeConflicts(routes []CompiledRoute) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("route path tree conflict detected while compiling routes: %v", r)
		}
	}()

	probe := gin.New()
	noop := func(*gin.Context) {}
	for _, r := range routes {
		probe.Handle(r.Spec.Method, r.FullPath, noop)
	}
	return nil
}
