// Package doc 以脚手架内置的 user 参考模块为例，说明业务逻辑在当前
// 「模块化 + 显式装配」结构下的分层落地方式。
//
// 分层口径以项目分层规范文档为单一来源；本文件只是 user 模块
// （资料查询/更新、会员校验、账号注销）这一具体场景的落地说明，供新增模块时对照参考
// （历史上的 handler/service/repository 三层口径已废弃）。
//
// 原则：依赖方向单向；业务编排在 application，后台调度在 worker（同样走 application，
// 不直连 repository），数据访问在 repository，装配在 runtime/module，进程编排在 bootstrap。
//
//	transport/http → application → repository
//	                     │
//	                     ├─▶ port: UserRepository / DeactivationRecorder / TxManager
//	                     └─▶ dto（对客请求/响应形状）
//	worker          → application（不直连 repository）
//	runtime/module/user：db → repo + dbtx.Manager → application service
//	bootstrap：把 runtime 装出的 service 接到 HTTP 路由与 worker 上
//
// =====================================================================
// 目录结构（user 模块相关，均为实际文件）
// =====================================================================
//
// internal/
// ├── models/
// │   ├── user.go                     ← User 结构体 + Status/VipStatus/Gender 枚举常量（不含业务方法）
// │   ├── user_profile.go             ← UserProfile 关联表
// │   └── user_deactivation.go        ← UserDeactivation 注销审计记录（方案 A：脱敏改写 + 软删）
// │
// ├── repository/                     ← GORM 数据访问，mdbCtx 统一经 dbtx.DB 收口
// │   ├── dbtx/dbtx.go                ← 事务句柄收口 DB(ctx, db) + Manager（实现 application.TxManager）
// │   ├── user/                       ← 用户仓储：Get / ExistsByID / Update /
// │   │                                 LockByID / AnonymizeForDeactivation / SoftDeleteProfile
// │   └── user_deactivation/          ← 注销记录仓储：Create / GetLatestByUserID
// │
// ├── module/user/
// │   ├── application/                ← 业务编排（只认 port，不依赖 gorm/repository）
// │   │   ├── service.go              ← Service：ValidateUser / ValidateVIP（可注入时钟）
// │   │   ├── profile.go              ← Profile 查询 / UpdateProfile 更新（+ maskPhone 脱敏展示）
// │   │   ├── deactivate.go           ← Deactivate 注销（事务编排）+ 领域 error
// │   │   └── ports.go               ← UserRepository / DeactivationRecorder / TxManager 三个 port
// │   ├── dto/user.go                 ← UserProfileResponse / UserUpdateRequest / UserDeactivateRequest
// │   └── transport/http/
// │       ├── handler.go              ← Handler：Get / Update / Deactivate + errMapper（领域 error→状态码）
// │       └── router.go               ← UserModule 实现 router.Module，注册 /user 路由
// │                                     （worker/ 为可选目录，母版未内置实例，见下「后台任务」）
// │
// └── runtime/module/user/module.go   ← 装配 NewService(db)：注入 repo + dbtx.Manager 得到 application.Service
//
// =====================================================================
// application 应用层能力（Service）
// =====================================================================
//
//	Profile          按用户主键查资料，脱敏手机号后返回 dto.UserProfileResponse
//	UpdateProfile    只更新显式给出的字段（文本非空 / Gender 非 nil），无字段则不触库
//	ValidateUser     校验用户存在且可用；库访问失败上抛 error（由调用方判 503），不静默降级
//	ValidateVIP      校验会员有效（存在 + vip_status=active + 未过期）
//	Deactivate       账号注销（事务编排，见下）
//
// =====================================================================
// Deactivate：事务边界在 application，*gorm.DB 不进 application
// =====================================================================
//
// 注销经 TxManager.Do 开启事务，fn 内拿到的 ctx 已携带事务句柄，repo 经各自 mdbCtx
// （dbtx.DB(ctx, r.db)）自动参与同一事务。事务内步骤：
//
//  1. LockByID 以 SELECT ... FOR UPDATE 锁行，规避并发重复注销
//  2. 已是注销态则幂等返回 ErrAlreadyDeactivated
//  3. DeactivationRecorder.Create 写 user_deactivations（保留原值供审计/申诉）
//  4. AnonymizeForDeactivation 脱敏改写 + 软删 users 行
//  5. SoftDeleteProfile 软删 user_profiles 关联行
//
// 任一步失败整事务回滚；SQL 细节全部下沉到 repository，application 只做编排。
//
// =====================================================================
// 后台任务：机制在 bootstrap，母版不内置业务实例
// =====================================================================
//
// bootstrap/worker.go 提供 Worker 接口（Name / Start）、WorkerManager（并发启动、
// 失败上报、等待退出）与 NewLegacyWorker（把 func(ctx) error 适配成 Worker）。
// buildWorkers 是装配第五阶段，母版默认返回空集合，只留 scaffold:module-workers
// 标记作为注册位——按业务需要在标记前追加一行 Worker 构造即可，无需改动函数签名。
//
// 新增 worker 遵守两条边界：放在 module/<m>/worker/ 下；只依赖 application，
// 不直连 repository / gorm（scripts/check-architecture.sh 检查该规则）。
// 任一 worker 的 Start 返回非 nil 错误，进程按 worker failed 退出，不做静默重启。
//
// 关停顺序（app.go，改动前先看 spec）：workerCtx 经 context.WithoutCancel 从信号 ctx
// 独立派生，启动 workers → HTTP、关停 HTTP → workers。这样 HTTP 优雅关停期间在飞请求
// 写入的任务仍能被 worker 消费；若让 workerCtx 跟随信号 ctx，worker 会先于 HTTP 退出
// 而丢任务。该顺序由 bootstrap-module-assembly spec 与 app_test.go 用例共同锁定。
//
// =====================================================================
// 依赖关系（单向）
// =====================================================================
//
//	transport/http/handler(Get/Update/Deactivate)
//	     │  ginc 取 userID → 调 application → resp 统一响应（errMapper 映射领域 error）
//	     ▼
//	application/Service ──▶ port: UserRepository / DeactivationRecorder / TxManager
//	     │                        （由 repository + dbtx.Manager 在 runtime 层实现并注入）
//	     ▼（Deactivate 经 TxManager.Do 开事务）
//	repository/{user,user_deactivation} ──▶ dbtx.DB(ctx, db)（mdbCtx 收口，事务内用事务句柄）
//
//	module/<m>/worker（如有）──▶ application/Service（不直连 repository）
//
//	runtime/module/user.NewService(db) 负责把上述装配起来：
//	  userrepo.New(db) + userdeactivation.New(db) + dbtx.NewManager(db) → application.NewServiceWithTx。
//	bootstrap/providers.go 再把 Service 接到 userhttp.NewModule（HTTP 路由）上；
//	  后台任务经 buildWorkers 的 scaffold:module-workers 标记按需接入。
package doc
