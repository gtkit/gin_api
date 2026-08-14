# 缓存

## 边界

- **缓存默认不是权威数据源。**
- 写 DB 成功后再更新/失效缓存。
- 缓存写失败必须有降级策略，常见做法是删除旧缓存让读路径回源。
- key 有命名空间和版本意识。
- value 只放读路径必要的字段，不无约束地缓存完整持久化模型。

本项目有两套缓存能力，解决的问题完全不同，不要混用。

## GET 响应缓存（`internal/pkg/httpcache`）

`gincache` 的装配器，**默认关闭**。

它把一次响应的**字节**原样发给后续请求——挂到带身份的路由上就是把 A 的数据发给 B，
不会报错、不会有异常日志，只会在某天被用户发现。

```go
// 只读路由显式挂上；未启用时 Middleware 返回透传，不必判空
v1.GET("/public/articles", cacheStager.Middleware(0), handler)
v1.GET("/public/hot", cacheStager.Middleware(5*time.Second), handler)  // 覆盖 TTL
```

三道防线缺一不可：

1. 配置门控（`cache.enabled` + `cache.store`）；
2. **只对显式挂了中间件的路由生效**，没有「全局开启」这条路径；
3. 每请求的身份判据——带 `Authorization` / `App-Token` / `Cookie` / `Range` 的一律绕过。

装配用的是 `gincache.Cache`，**不是** `CacheByRequestURI`：后者按 URI 直接组键，
根本不调用策略函数，身份判据会被整个绕过，**而配置看起来完全正常**。

其他约束：只缓存 200（缓存 4xx/5xx 会把一次瞬时故障固定住整个 TTL）；
键含方法与排序后的查询串；`memory` 后端走 ristretto（有界准入/淘汰），
`redis` 后端的有界性由 Redis 的 maxmemory 策略负责。

### 挂载者必须自己保证的三条

底层的缓存判据是**请求期**的，拿不到响应头，而缓存写入发生在 handler 返回之后。
因此下面三条无法由中间件兜住：

1. **handler 不得设置 `Set-Cookie`**——它会被缓存并原样发给后续所有请求，
   即第一个用户的会话被发给其他人。
2. **不得依赖 `Cache-Control: private` / `no-store` 阻止缓存**——底层不看这个头，
   声明了也照缓存。
3. **不得依赖 `Vary`**——缓存键里没有它列出的请求头，同一 URL 对不同
   `Accept-Language` / `Accept-Encoding` 会回放同一份。

本包在缓存中间件外层加了一道**响应期审计**：命中上述任一条即记 Error 日志。
它**挡不住泄漏**，只能让误用在日志里可见。

## 进程内缓存（`internal/pkg/cache`）

泛型 `GetOrLoad`，一次性解决击穿、雪崩、无界三件事：

```go
c, err := cache.New[Article](1000, time.Minute)   // 容量与 TTL 都必填且必须为正

key := strconv.FormatInt(articleID, 10)           // key 是 string，不是任意类型
article, err := c.GetOrLoad(ctx, key, func(ctx context.Context) (Article, error) {
    return repo.FindByID(ctx, articleID)
})
c.Invalidate(key)                                  // 写 DB 成功后主动失效
```

- **singleflight**：同一个键的并发加载只执行一次。热点键过期的瞬间所有并发请求同时回源，
  一次 miss 会被放大成 N 次查询——而那一刻正是流量最高的时候。
- **TTL 抖动**（默认 ±10%）：一批同时写入的键不会集体过期，回源尖峰不再周期性重演。
- **容量上限 + LRU**：无界缓存在键空间随请求变化时会一直涨。那不是命中率问题，
  是一次必然发生的 OOM。
- **失败不缓存**：缓存下来的错误会被固定住整个 TTL，故障早已恢复而调用方还在拿错误。
- **取消只影响当前调用方**：loader 拿到的是 `context.WithoutCancel` 的 ctx，
  一个提前放弃的调用方不会连累其他等待者。

三个问题缺一都会在生产上被发现，而且都是在流量最高的时候。

## Redis / Pub-Sub / 延迟队列

- Redis-only repo 不参与 GORM 事务；原子性用 Lua / SetNX / 原子计数。
- **Pub/Sub 只做通知，不做权威状态**：客户端收到事件后仍应能查询最终状态。
  Pub/Sub 不保证送达，把状态流转建立在它上面，一次断连就是一条永久丢失的状态变更。
- 延迟队列任务必须有兜底扫描或补偿路径，避免单点丢任务。

## 测试要求

涉及缓存的变更至少覆盖：正常路径、外部依赖失败、context cancel / timeout、
重复执行或并发竞争。响应缓存另需覆盖身份请求绕过、非 200 不缓存两条。
