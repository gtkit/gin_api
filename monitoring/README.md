# 监控与性能基线

此目录是部署模板，不会自动启动 Collector、Prometheus 或 Grafana。使用部署方已有组件，先确认版本支持所用字段，再在非生产环境验收。

`collector.yml` 只接收 OTLP/HTTP metrics，端口 4318/9464 默认只监听 loopback。应用配置设置 telemetry.enabled=true、metrics=true、traces=false、endpoint="127.0.0.1:4318"、insecure=true；明文只用于同机通信。跨机必须配置认证/TLS 和私有网络后再调整监听地址。示例 memory_limiter 的 256 MiB 限额属于 Collector，不是应用限额。

每个应用副本设置唯一的 `OTEL_RESOURCE_ATTRIBUTES=service.instance.id=<replica-id>`；service.name 来自 application.name。Exporter 仅将这两个 resource 字段附加为标签，避免随意引入高基数属性。`translation_strategy=UnderscoreEscapingWithSuffixes` 固定名称规则：点转下划线，counter 加 `_total`，秒/字节分别加 `_seconds` / `_bytes`；直方图继续使用 `_bucket`、`_count`、`_sum`。此字段及 resource_constant_labels 已对照 [Collector 官方配置](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/exporter/prometheusexporter)；本机未运行 Collector，部署版本兼容仍需验证。

Prometheus 抓取同机 127.0.0.1:9464/metrics 并加载 prometheus-rules.yml。应用副本归属使用 service_name/service_instance_id，不能把 Collector 抓取 instance 当作应用实例。导入 dashboard.json 后选择 Prometheus 数据源和服务。模板使用 Grafana Classic JSON，参考 [官方模型](https://grafana.com/docs/grafana/latest/visualizations/dashboards/build-dashboards/view-dashboard-json-model/)。

告警覆盖错误率、p99、准入拒绝、密码容量、MySQL 使用率和 Redis 超时。1%/1 秒/90% 等是示例阈值，不是已经测得的 SLO。加载前按业务预算校准，再执行 `promtool check rules monitoring/prometheus-rules.yml` 并演练触发/恢复。内存看板展示 Go runtime 使用量，不是 RSS；RSS/容器限额应从宿主或容器监控取得，尚无该证据时不设置伪精确内存告警。

## 负载基线

复用 `scripts/load-api.go`，不另建压测框架。它限速、限制并发和响应体、拒绝默认非 loopback 目标，报告包括生成器丢弃、传输错误及调度起点延迟。报告只写参数和统计，不回显 URL、请求正文、认证头、场景路径或本机目录。

确认本机非生产应用已经启动后执行：

```sh
go run -trimpath scripts/load-api.go -target http://127.0.0.1:8008 \
  -scenario monitoring/health-scenario.json -rate 10 -concurrency 2 -duration 30s
```

上例只测试探针链路，不代表认证/数据库业务容量。业务基线需要自行提供脱敏场景，分开测试读、写、密码及依赖变慢，记录数据量、硬件配额、副本数、数据库池与配置版本。逐档增加 offered rate；当 generator_dropped/transport_errors 非零时，先排除生成器/网络瓶颈，不能把该档当成服务能力上限。

输出的 p95/p99 是直方图桶上界估计；overflow=true 表示没有有限估计，不是 0ms。service_resource_evidence 明确为 not_collected：工具未采集服务 CPU、RSS、DB 等，禁止用生成器自身的资源消耗替代。将同时间窗 Prometheus 导出、宿主 RSS 和数据库慢查询证据与报告并存，才能评估容量。

当前交付提供真实 loopback 工具回归及静态模板检查；没有生产容量数字，没有声称 Grafana 已导入、告警已触发或真实数据库压测已通过。
