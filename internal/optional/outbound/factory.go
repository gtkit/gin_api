// Package outbound 按名字提供出站 HTTP 客户端。
//
// 它解决的是同一类反复出现的出站缺陷：调用点各自 new 一个客户端。那样每次
// 调用都新建一套连接池，连接无法复用，高频调用下本地端口会被 TIME_WAIT 占满；
// 而且超时与响应体上限散落在每个调用点，漏掉一处就是一个能把进程拖垮的洞。
//
// 契约与 internal/optional/outbound/breaker 对称：**下游集合是启动期事实**。工厂在构造时
// 按配置把全部客户端建好，运行期只能按名字取；取不到就是编码错误，而不是
// 悄悄再建一个。名字即故障域，与熔断器的下游名一一对应——两者用同一个名字，
// "这个下游的连接池"与"这个下游的熔断器"才指的是同一件事。
package outbound

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/gtkit/httpc"
)

// ErrUnknownClient 表示按名字取用了一个装配期没有登记的客户端。
//
// 单独成错而不是返回 nil：nil 客户端会在调用点变成一次 panic 或一次
// 静默的空指针分支，而这里的真实原因是配置里没有这个名字。
var ErrUnknownClient = errors.New("httpclient: downstream is not registered")

// Factory 持有全部具名客户端，构造后不可变。
//
// 并发安全：内部 map 在构造后只读，httpc.Client 自身也是并发安全的。
type Factory struct {
	clients map[string]*httpc.Client

	// closeOnce 保证重复关闭无害：关停路径上多处 defer 收尾很常见。
	closeOnce sync.Once
}

// New 按配置构造全部客户端。
//
// clients 为空时返回一个空工厂而不是 nil：调用方可以无条件注入它，
// 取用未登记的名字会得到 ErrUnknownClient——那正是此时该有的答案。
func New(cfg Config) (*Factory, error) {
	factory := &Factory{clients: make(map[string]*httpc.Client, len(cfg.Clients))}

	// 按名字排序构造：出错时的报错顺序与配置无关，可复现。
	for _, name := range slices.Sorted(maps.Keys(cfg.Clients)) {
		resolved := cfg.Clients[name].Resolved(cfg.Defaults)
		if resolved.BaseURL == "" {
			// 正常路径由配置校验拦下；这里兜底是因为工厂也可能被直接构造。
			return nil, fmt.Errorf("httpclient: client %q has no base_url", name)
		}

		factory.clients[name] = httpc.New(
			httpc.WithBaseURL(resolved.BaseURL),
			// 总超时必须设：没有它，一个卡住的下游会把本进程的 goroutine
			// 与连接一起扣住，最终表现为自己被拖垮而不是下游不可用。
			httpc.WithTimeout(resolved.Timeout),
			// 响应体上限把"下游返回了一个异常巨大的响应"从一次 OOM
			// 变成一个明确的错误。
			httpc.WithMaxResponseBytes(resolved.MaxResponseBytes),
			// 每个名字独立池：一个下游的慢响应不该占用另一个下游的连接配额。
			httpc.WithMaxConnsPerHost(resolved.MaxConnsPerHost),
			httpc.WithMaxIdleConnsPerHost(resolved.MaxIdleConnsPerHost),
		)
		client := factory.clients[name].HTTPClient()
		client.Transport = &propagationTransport{base: client.Transport}
	}

	return factory, nil
}

// Client 返回具名客户端。
//
// 同名多次取到的是**同一个实例**——复用连接池正是本包存在的理由。
func (f *Factory) Client(name string) (*httpc.Client, error) {
	if f == nil {
		return nil, fmt.Errorf("%w: %s", ErrUnknownClient, name)
	}
	client, ok := f.clients[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s (registered: %v)", ErrUnknownClient, name, f.Names())
	}
	return client, nil
}

// Names 返回已登记的下游名（有序）。
func (f *Factory) Names() []string {
	if f == nil {
		return nil
	}
	return slices.Sorted(maps.Keys(f.clients))
}

// Close 释放空闲连接。
//
// 关停时不释放，空闲连接会一直挂到对端超时；在滚动重启的场景里，那意味着
// 旧实例的连接还占着下游的连接配额，而新实例正在建新的。
func (f *Factory) Close() error {
	if f == nil {
		return nil
	}
	f.closeOnce.Do(func() {
		for _, client := range f.clients {
			client.HTTPClient().CloseIdleConnections()
		}
	})
	return nil
}
