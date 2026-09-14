// Package user 是脚手架内置的参考模块。本文件说明业务逻辑在当前
// 「模块化 + 显式装配」结构下的分层落地方式。
//
// 它随模块一起存在：说明与被说明的代码放在同一个目录，模块被删除时
// 说明一并消失。此前它是一个独立的 doc 包，于是 user 模块被替换后，
// 那份描述会留在仓库里继续描述一个已经不存在的结构。
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
// │   ├── transport/http/
// │   │   ├── handler.go              ← Handler：Get / Update / Deactivate + errMapper（领域 error→状态码）
// │   │   └── router.go               ← Register(r, Deps)：在 /api/v1 组上用 gin 原生方式注册 /user 路由
// │   └── worker/
// │       └── deactivation_cleanup.go ← DeactivationCleanup：按保留期分批清理注销档案（纯业务，只认 port）
// │
// └── runtime/module/user/
//
//	├── module.go                   ← NewService(db)：注入 repo + dbtx.Manager 得到 application.Service（依赖不全即报错）
//	└── cleanup.go                  ← NewDeactivationCleanup(db, cfg)：未配置保留期时返回 nil，不注册
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
// 后台任务：机制在 bootstrap，业务在 worker/，两者经 runtime 与 bootstrap 的薄适配器相接
// =====================================================================
//
// bootstrap 提供 Worker 接口（Name / Kind / Required / Lifecycle / Prepare / Run / Cleanup）
// 与 WorkerManager（注册期校验声明、分阶段启动、Cleanup 恰好一次、按类别分相关停）。
// 本模块的注销档案清理走这条链：worker/DeactivationCleanup 只含业务（RunOnce / Interval /
// Health），runtime/module/user/cleanup.go 把 db + config 翻译成它，bootstrap 的
// newDeactivationCleanupWorker 再套上单实例租约与 Kind / Required / Lifecycle 声明。
// 未配置保留期时 runtime 返回 nil，bootstrap 据此不注册——禁用表现为"不存在"，
// 而不是一个空转的实例。其余后台任务经 scaffold:module-workers 标记按同样的
// 三段式接入，无需改动 buildWorkers 的签名。
//
// 新增 worker 遵守两条边界：放在 module/<m>/worker/ 下；只依赖 application，
// 不直连 repository / gorm（scripts/check-architecture.sh 检查该规则）。
//
// 关停顺序由 runtime-state-machine spec 约束：
// 停 producer → 等 LB 传播 → 关 HTTP → drain consumer → 停 bootstrap → 清理 → 关资源。
// producer 必须先停（否则它在 HTTP 关停期间继续生产任务），consumer 必须后停
// （否则 HTTP 关停期间在飞请求写入的任务会丢）。
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
//	module/user/worker/DeactivationCleanup ──▶ port: DeactivationPurger（不直连 repository）
//
//	runtime/module/user 负责把上述装配起来：
//	  NewService(db)：userrepo.New(db) + userdeactivation.New(db) + dbtx.NewManager(db) → application.NewService；
//	  NewDeactivationCleanup(db, cfg)：userdeactivation.New(db) → worker.DeactivationCleanup。
//	bootstrap/providers.go 再经 userhttp.Register(v1, userhttp.Deps{...}) 注册 HTTP 路由，
//	  经 buildWorkers 把清理任务包成单实例 Worker 交给 WorkerManager。
package user
