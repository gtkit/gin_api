package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	logger "github.com/gtkit/logger/v2"
)

// RequestIDHeader 是请求 ID 的响应头名。
//
// 固定为常量而不是可配置项：可配置的 header 名意味着同一进程内不同 engine
// 可能用不同的名字，而日志关联、客户端排障、网关规则全都依赖它稳定。
const RequestIDHeader = "X-Request-ID"

// RequestIDLength 是生成的 ID 的十六进制字符数：128-bit 进程前缀 + 64-bit 计数。
//
// 导出它是因为"定长"本身是对下游的承诺：日志字段宽度稳定、
// 校验正则不必处理变长。改动它等于改动一条对外契约。
const RequestIDLength = 32 + 16

// RequestIDContextKey 是内部请求 ID 在 gin.Context 中的键。
const RequestIDContextKey = "request.id"

// ErrRequestIDExhausted 表示单进程内的请求计数已耗尽。
//
// 2^64 个请求在任何真实进程里都不可能达到，因此它实际上是一道
// **fail-closed 的断言**：真出现说明计数器被以某种方式破坏了，
// 此时继续发号会产生重复 ID，而重复 ID 会让日志关联给出错误的结论——
// 那比没有 ID 更糟。
var ErrRequestIDExhausted = errors.New("request id counter exhausted")

// RequestIDGenerator 产出进程内唯一、且跨进程碰撞概率可忽略的请求 ID。
//
// 结构：`<128-bit 进程随机前缀><64-bit 单调计数>`，定长 48 个小写十六进制字符。
//
// 为什么自建而不用 gin-contrib/requestid：
//
//  1. 该包会**原样复用**客户端传来的 X-Request-ID。于是攻击者可以伪造关联 ID
//     把自己的请求日志与他人的串在一起，也可以传一个超长值制造巨大的日志字段。
//  2. 它把 header 名存在**包级全局变量**里，同一进程内多个 engine 或不同配置
//     会互相影响。
//  3. 它在请求路径上取随机数，于是每个请求都带一个随机源失败分支。
//
// 本实现把随机只用在**启动期**（读一次 128-bit 进程前缀，失败即启动失败），
// 请求路径上只有一次原子自增——没有失败分支，也没有锁。
type RequestIDGenerator struct {
	prefix  string
	counter atomic.Uint64
}

// NewRequestIDGenerator 读取进程随机前缀。
//
// 随机源失败即返回错误：没有前缀就只能退回时间戳或弱随机，
// 而那会让不同实例产出相同的 ID 序列，跨实例的日志关联随之失效。
// 这个失败必须在启动期暴露，绝不允许降级。
func NewRequestIDGenerator() (*RequestIDGenerator, error) {
	var seed [16]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return nil, fmt.Errorf("seed request id generator: %w", err)
	}
	return &RequestIDGenerator{prefix: hex.EncodeToString(seed[:])}, nil
}

// Next 返回下一个请求 ID。
func (g *RequestIDGenerator) Next() (string, error) {
	n := g.counter.Add(1)
	if n == 0 {
		// Add 回绕到 0 说明已用满 2^64；继续发号会重复。
		return "", ErrRequestIDExhausted
	}
	return fmt.Sprintf("%s%016x", g.prefix, n), nil
}

// RequestID 为每个请求分配内部 ID，并写入请求头、响应头与两个 context。
//
// **不信任客户端输入**：传入的 X-Request-ID 不作为内部 ID 使用。跨服务传播
// 由标准 W3C trace context 负责；确需保留上游 ID 时，应单独存为经过长度与
// 字符集校验的 upstream_request_id，且只在请求来自可信代理时记录，
// 不参与日志关联的主键。
//
// 写入请求头是有意的：下游中间件与 handler 读 header 就能拿到内部 ID，
// 不必都去读 context。
func RequestID(gen *RequestIDGenerator) (gin.HandlerFunc, error) {
	if gen == nil {
		return nil, errors.New("request id middleware requires a generator")
	}

	return func(c *gin.Context) {
		id, err := gen.Next()
		if err != nil {
			// 计数耗尽属不可能事件；真发生时宁可不带 ID 也不发重复 ID。
			logger.Errorf("request id generation failed: %v", err)
			c.Next()
			return
		}

		c.Request.Header.Set(RequestIDHeader, id)
		c.Writer.Header().Set(RequestIDHeader, id)
		c.Set(RequestIDContextKey, id)
		c.Request = c.Request.WithContext(
			logger.ContextWithRequestID(c.Request.Context(), id))

		c.Next()
	}, nil
}

// RequestIDFromContext 从 gin 上下文取内部请求 ID。
func RequestIDFromContext(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if v, ok := c.Get(RequestIDContextKey); ok {
		if id, ok := v.(string); ok {
			return id
		}
	}
	return c.Request.Header.Get(RequestIDHeader)
}
