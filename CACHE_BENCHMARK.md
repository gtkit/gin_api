# Cache Benchmark And Route Strategy

本文档记录当前项目中适合接入 `gincache` 的接口、压测流程，以及本地基线结果。

## 1. 当前已接入 `gincache` 的路由

当前已经接入缓存的只读 GET 路由：

| 路由 | 模块 | 缓存方式 | TTL |
|------|------|----------|-----|
| `/api/v1/account/aim` | account | `CacheByPath()` | 10m |
| `/api/v1/account/type` | account | `CacheByPath()` | 10m |
| `/api/v1/common/shortcut` | common | `CacheByPath()` | 30s |
| `/api/v1/common/topic` | common | `CacheByPath()` | 30s |
| `/api/v1/common/navbar` | common | `CacheByPath()` | 30s |
| `/api/v1/products` | product | `CacheByPath()` | 5s |
| `/api/v1/product` | product | `CacheByPath()` | 5s |
| `/api/v1/articles?menu_id=...` | article | `CacheByURI()` | 15s |
| `/api/v1/article?menu_id=...` | article | `CacheByURI()` | 15s |

当前同时暴露了只读缓存统计接口：

| 路由 | 统计范围 |
|------|----------|
| `/api/v1/account/cache/stats` | account 模块缓存命中情况 |
| `/api/v1/common/cache/stats` | common 模块缓存命中情况 |
| `/api/v1/products/cache/stats` | product 模块缓存命中情况 |
| `/api/v1/product/cache/stats` | product 兼容路由缓存命中情况 |
| `/api/v1/articles/cache/stats` | article 列表缓存命中情况 |
| `/api/v1/article/cache/stats` | article 兼容路由缓存命中情况 |

## 2. 明确不加缓存的路由

以下路由当前不建议接 `gincache`：

| 路由 | 原因 |
|------|------|
| `/api/v1/common/ping` | 实时性价值高于缓存价值，逻辑极轻 |
| `/api/v1/articles/:id` | 会递增浏览量，且受登录态影响 |
| `/api/v1/article/:id` | 同上 |
| `/api/v1/user` | 强用户态 |
| `/api/v1/orders/:order_token/status` | 强动态 |
| `/api/v1/orders/:order_token/timeline` | 强动态 |
| `/api/v1/llm/*` | 会话态 / 流式接口 |
| `/api/v1/bdvoice/*` | 鉴权态 / 实时语音能力 |

## 3. 当前 `gincache` 接法

项目当前统一采用新版更推荐的接法：

1. 先创建 `persist.NewRedisStore(...)`
2. 显式配置 Redis 读写超时
3. 再创建 `gincache.New(...)`
4. 用 `middleware.CacheByPath()` 或 `middleware.CacheByURI()`

示例（membership 模块）：

```go
store := persist.NewRedisStore(
    redisClient,
    persist.WithKeyPrefix("sleep:client:cache"),
    persist.WithReadTimeout(2*time.Second),
    persist.WithWriteTimeout(2*time.Second),
)

cacheMiddleware := gincache.New(
    store,
    5*time.Second,
    gincache.WithOnHitCache(...),
    gincache.WithOnMissCache(...),
    gincache.WithOnShareSingleFlight(...),
    gincache.WithMaxBodySize(1<<20),
)
```

## 4. 压测环境

本次压测使用：

- Go 服务：当前工作区代码
- 配置：`config/env/productbench.yml`
- 监听端口：`8011`
- 压测工具：`ab`
- 本地地址：`http://127.0.0.1:8011`

启动命令：

```bash
/usr/local/go/bin/go run . server -c productbench
```

## 5. 压测流程

### Step 1. 启动服务

```bash
/usr/local/go/bin/go run . server -c productbench
```

### Step 2. 先做一次预热请求

示例：

```bash
curl -sS http://127.0.0.1:8011/api/v1/products > /dev/null
```

### Step 3. 使用 `ab` 压测

本次统一参数：

- 请求数：`500`
- 并发：`20`

示例：

```bash
ab -n 500 -c 20 http://127.0.0.1:8011/api/v1/products
ab -n 500 -c 20 http://127.0.0.1:8011/api/v1/account/aim
ab -n 500 -c 20 "http://127.0.0.1:8011/api/v1/articles?menu_id=1"
```

### Step 4. 查看缓存统计

当前已暴露只读缓存统计接口，示例：

```bash
curl -sS http://127.0.0.1:8011/api/v1/account/cache/stats
curl -sS http://127.0.0.1:8011/api/v1/common/cache/stats
curl -sS http://127.0.0.1:8011/api/v1/products/cache/stats
curl -sS "http://127.0.0.1:8011/api/v1/articles/cache/stats?menu_id=1"
```

返回字段：

- `hit`
- `miss`
- `singleflight_share`
- `hit_rate`
- `served_rate`

说明：

- `hit_rate = hit / (hit + miss)`
- `served_rate = (hit + singleflight_share) / (hit + miss)`

其中 `served_rate` 更适合判断缓存层整体是否已经挡住了大部分请求。

说明：

- `hit`：直接缓存命中
- `singleflight_share`：在缓存未命中阶段复用了相同结果的请求
- `served_rate`：更适合判断整体上缓存层是否已经足够挡住流量

## 6. 本地基线结果

本次实际压测得到的数据如下：

| 接口 | RPS | 平均延迟(ms) | P50(ms) | P95(ms) | P99(ms) |
|------|-----|--------------|---------|---------|---------|
| `/api/v1/account/aim` | 4991.22 | 4.007 | 3 | 6 | 13 |
| `/api/v1/account/type` | 6647.96 | 3.008 | 3 | 4 | 6 |
| `/api/v1/common/shortcut` | 9553.65 | 2.093 | 2 | 5 | 8 |
| `/api/v1/common/topic` | 7976.26 | 2.507 | 2 | 4 | 5 |
| `/api/v1/common/navbar` | 3881.72 | 5.152 | 3 | 10 | 48 |
| `/api/v1/products` | 4475.55 | 4.469 | 4 | 7 | 15 |
| `/api/v1/articles?menu_id=1` | 4057.95 | 4.929 | 4 | 12 | 15 |

额外顺序压测（非并发）：

```bash
ab -n 200 -c 1 http://127.0.0.1:8011/api/v1/product
```

结果：

- 平均延迟：`0.703ms`
- P99：`2ms`
- Max：`5ms`

`/api/v1/products/cache/stats` 在压测后的示例值：

```json
{
  "code": 0,
  "msg": "OK",
  "data": {
    "hit": 500,
    "miss": 1,
    "singleflight_share": 0,
    "hit_rate": 0.998003992015968,
    "served_rate": 0.998003992015968
  }
}
```

## 7. 结论：当前不建议切 `TwoLevelStore`

基于本次数据，当前阶段不建议把 membership 商品列表直接升级到 `TwoLevelStore`。

原因：

1. 单层 RedisStore 已经很快

- 顺序命中平均只有 `~0.7ms`
- 并发压测下 `/products` 平均延迟也只有 `~4.5ms`

2. 当前接口规模不大

- 会员商品列表接口返回体小
- TTL 短
- 业务逻辑简单

3. 还没有看到 Redis 成为明显瓶颈

- 现有延迟和吞吐都在很合理的范围内
- 现在引入 L1 本地缓存收益不明显

4. `TwoLevelStore` 会带来额外复杂度

- 本地缓存一致性
- 多层缓存排障
- 内存占用
- 统计解释复杂度

## 8. 什么时候再考虑 `TwoLevelStore`

当出现以下情况时，再考虑升级：

- `/products` 成为首页高频热点接口
- Redis RTT 明显影响接口延迟
- 多实例下重复读取流量非常高
- 你有明确压测结果表明 L1 本地缓存能显著降低 P95/P99

## 9. 注意事项

当前压测实例运行时，后台订单补偿 worker 可能会输出与订单表结构相关的错误日志。

原因是：

- 当前本地数据库里旧订单数据仍可能存在
- 订单模块已经引入了 `benefit_vip_type / benefit_vip_days` 快照字段
- 如果本地数据库 schema 未同步到最新订单结构，后台 worker 会在补偿扫描时打出字段缺失日志

这不会影响本次这些只读缓存接口的 benchmark 结果，但建议在后续本地联调时同步最新订单 schema，避免日志噪音干扰判断。

## 10. 建议的后续动作

当前建议顺序：

1. 保持现有单层 RedisStore
2. 继续观察这些统计接口：
   - `/api/v1/account/cache/stats`
   - `/api/v1/common/cache/stats`
   - `/api/v1/products/cache/stats`
   - `/api/v1/articles/cache/stats`
3. 对真正热点接口做多实例压测
4. 等出现明确热点和压测证据后，再评估是否切换到 `TwoLevelStore`
