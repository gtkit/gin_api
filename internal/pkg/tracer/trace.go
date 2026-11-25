package tracer

// const (
// 	jaegerEndpoint = "127.0.0.1:4318"
// )
//
// var Tracer = otel.Tracer(config.GetString("app.name")) // 初始化 Tracer
//
// func StartTracer() func() {
// 	ctx := context.Background()
// 	// 初始化并配置 Tracer
// 	tp, err := InitTracer(ctx)
// 	if err != nil {
// 		logger.Fatalf("initTracer failed, err:%v\n", err)
// 	}
// 	logger.Green("Tracer started")
// 	return func() {
// 		if err := tp.Shutdown(ctx); err != nil {
// 			logger.Fatalf("shutting down tracer provider failed, err:%v\n", err)
// 		}
// 	}
// }
//
// // newJaegerTraceProvider 创建一个 Jaeger Trace Provider
// func newJaegerTraceProvider(ctx context.Context) (*sdktrace.TracerProvider, error) {
// 	// 创建一个使用 HTTP 协议连接本机Jaeger的 Exporter
// 	exp, err := otlptracehttp.New(ctx,
// 		otlptracehttp.WithEndpoint(jaegerEndpoint),
// 		otlptracehttp.WithInsecure())
// 	if err != nil {
// 		return nil, err
// 	}
// 	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName(config.GetString("app.name"))))
// 	if err != nil {
// 		return nil, err
// 	}
// 	traceProvider := sdktrace.NewTracerProvider(
// 		sdktrace.WithResource(res),
// 		sdktrace.WithSampler(sdktrace.AlwaysSample()), // 采样
// 		sdktrace.WithBatcher(exp, sdktrace.WithBatchTimeout(time.Second)),
// 	)
// 	return traceProvider, nil
// }
//
// // initTracer 初始化 Tracer
// func InitTracer(ctx context.Context) (*sdktrace.TracerProvider, error) {
// 	tp, err := newJaegerTraceProvider(ctx)
// 	if err != nil {
// 		return nil, err
// 	}
//
// 	otel.SetTracerProvider(tp)
// 	otel.SetTextMapPropagator(
// 		propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}),
// 	)
// 	return tp, nil
// }
