package ginc

import "github.com/gin-gonic/gin"

// ClientIP 返回本次请求的客户端 IP，是全仓取客户端 IP 的**唯一入口**。
//
// 生产代码除本函数外禁止直接调用 gin 的 ClientIP()，由架构传感器强制。
// 收口的三个理由（都不是风格问题）：
//
//  1. 口径一致：IP 同时是审计事实与限流输入。两处各自取值时，任何一处加了归一化
//     （IPv6 压缩、IPv4-mapped 处理）就会出现"日志里的 IP"与"被限流的 IP"指向不同主体。
//  2. 单一改动点：后续要给限流 key 做 HMAC、给观测做脱敏，只需改这里。
//  3. 传感器可执行：有了唯一入口，"禁止裸调 ClientIP()"才是个能自动检查的判据；
//     否则只能写"请正确配置代理"这种无法验证的约定。
//
// 取值的安全性由 engine 构造时的可信代理配置保证：可信代理集合为空（默认）时，
// 本函数只返回真实 TCP peer 地址，客户端伪造的 X-Forwarded-For 一律被忽略。
func ClientIP(c *gin.Context) string {
	if c == nil {
		return ""
	}
	return c.ClientIP()
}
