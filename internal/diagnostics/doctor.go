// Package diagnostics 提供部署前/故障排查用的运行时自检。
//
// 它的输出是**机器可读**的：JSON schema 固定、字段稳定排序、只输出稳定的错误码
// 与脱敏摘要。人肉阅读也能用，但格式的第一消费者是脚本与告警。
package diagnostics

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/runtime/resource"

	"golang.org/x/sync/errgroup"
)

// CheckStatus 是单项检查的固定结论。
type CheckStatus string

const (
	// StatusPass 表示检查通过。
	StatusPass CheckStatus = "pass"
	// StatusFail 表示检查失败。
	StatusFail CheckStatus = "fail"
	// StatusDegraded 表示可选能力不可用，但不影响进程对外服务。
	StatusDegraded CheckStatus = "degraded"
	// StatusSkipped 表示本次未执行（如未启用该能力，或未开启 --deep）。
	StatusSkipped CheckStatus = "skipped"
)

// 错误码是**稳定枚举**，不是自由文本。
//
// 它会进告警规则与工单模板；自由文本一改，下游的匹配就全部失效，
// 而失效的方式是静默的——规则永远匹配不上，于是告警再也不响。
const (
	CodeOK                = "ok"
	CodeConfigInvalid     = "config_invalid"
	CodeUnreachable       = "unreachable"
	CodeTimeout           = "timeout"
	CodePermission        = "permission"
	CodeMissing           = "missing"
	CodeInsecurePerm      = "insecure_permission"
	CodeInconsistent      = "inconsistent_config"
	CodeCapabilityOff     = "capability_disabled"
	CodeNotRequestedDeep  = "deep_not_requested"
	CodeInternalDiagError = "diagnostic_error"
)

// CheckResult 是单项检查的结果。
//
// 字段集合固定：下游按 schema 解析，可选字段会让解析方不得不写两套分支。
type CheckResult struct {
	Name      string      `json:"name"`
	Status    CheckStatus `json:"status"`
	LatencyMS int64       `json:"latency_ms"`
	ErrorCode string      `json:"error_code"`
	// Detail 是**脱敏摘要**：绝不包含密钥、密码、完整 DSN 或原始错误串。
	Detail string `json:"detail"`
}

// Report 是一次 doctor 运行的完整输出。
type Report struct {
	// SchemaVersion 让下游能在 schema 演进时做兼容判断。
	SchemaVersion int           `json:"schema_version"`
	Deep          bool          `json:"deep"`
	Checks        []CheckResult `json:"checks"`
}

// reportSchemaVersion 是当前输出 schema 的版本号。
const reportSchemaVersion = 1

// 退出码约定（写死，供脚本判定）：
//
//	0 = 全部检查通过
//	2 = 仅 optional 能力 degraded/skipped
//	1 = 配置非法、required 检查失败，或 doctor 自身执行/编码失败
const (
	ExitOK       = 0
	ExitFailed   = 1
	ExitDegraded = 2
)

// ExitCode 由报告推导退出码。
func (r Report) ExitCode() int {
	degraded := false
	for _, c := range r.Checks {
		switch c.Status {
		case StatusFail:
			return ExitFailed
		case StatusDegraded:
			degraded = true
		case StatusPass, StatusSkipped:
		}
	}
	if degraded {
		return ExitDegraded
	}
	return ExitOK
}

// Options 控制一次 doctor 运行。
type Options struct {
	// Deep 打开需要真实 I/O 的检查（连通性、目录可写）。
	//
	// 默认关闭：doctor 会被放进 project init 这类"只想确认配置没写错"的路径，
	// 而那些环境往往连不上生产依赖。默认就打真实连接会让它在正常情况下报错。
	Deep bool
	// Timeout 是单项检查的上限。
	Timeout time.Duration
	// Probes 提供 deep 检查的执行体；为 nil 时 deep 检查记为 skipped。
	Probes Probes
	// RedisRequired 表示当前配置下 Redis 是**必需**依赖，探测失败即 fail 而非 degraded。
	//
	// **由调用方注入，不在本包推导**：必需性取决于"哪些依赖 Redis 的能力被启用了"，
	// 而那张能力表在装配层（bootstrap.RedisRequired）。本包自己再算一遍必然与它
	// 分叉，而分叉的两份里诊断这份永远不会跟上——它此前被硬编码为 false，于是
	// auth.mode: local 的部署在 Redis 挂掉时得到退出码 2「仅降级」，运维脚本据此
	// 放行，紧接着服务因必需 Redis 失败而根本起不来。
	//
	// 注入而非 import bootstrap：诊断层的价值之一是在装配起不来时仍然可用，
	// 不该反向依赖装配层。命令层同时看得到两边，是唯一不产生第二份实现的注入点。
	RedisRequired bool
	// ProbesUnavailable 表示 deep 检查的探测能力**建立失败**（如数据库连不上
	// 导致运行时起不来），与"没有注入探针"区分开。
	//
	// 两者都表现为 Probes 为空，但结论相反：没注入是装配选择，记 skipped 正确；
	// 建立失败恰恰是 deep 检查要报告的结论本身，必须记为失败，否则退出码为 0，
	// 而运维脚本按退出码判定，会把一台连不上依赖的机器判成全通。
	//
	// 用 bool 而不是 error：构建错误常带完整 DSN（含用户名，有时含密码），
	// 而报告是会被贴进工单的。原始错误由命令层打到 stderr，不进结构化报告。
	ProbesUnavailable bool
}

// Probes 是 deep 检查需要的真实 I/O 能力，由调用方注入。
//
// 注入而不是在本包内建连：本包不应该知道如何建立数据库连接，
// 那是资源装配的职责，复制一份就会与真正的装配路径漂移。
type Probes struct {
	PingDB    func(context.Context) error
	PingRedis func(context.Context) error
}

const defaultCheckTimeout = 3 * time.Second

// Run 执行自检并返回报告。
//
// 只读不修改：doctor 会在生产机器上被随手执行，任何写操作都可能造成它本想诊断的故障。
// 每项检查独立超时，互不影响；全部检查并发执行后按名称稳定排序。
func Run(ctx context.Context, cfg *config.Config, opts Options) Report {
	if opts.Timeout <= 0 {
		opts.Timeout = defaultCheckTimeout
	}

	report := Report{SchemaVersion: reportSchemaVersion, Deep: opts.Deep}

	if cfg == nil {
		report.Checks = []CheckResult{{
			Name: "config", Status: StatusFail, ErrorCode: CodeConfigInvalid,
			Detail: "configuration is not loaded",
		}}
		return report
	}

	checks := buildChecks(cfg, opts)
	results := make([]CheckResult, len(checks))

	group := new(errgroup.Group)
	for i, chk := range checks {
		group.Go(func() error {
			results[i] = runCheck(ctx, chk, opts.Timeout)
			return nil
		})
	}
	// 各子任务恒返回 nil：一项检查失败不该取消其余检查——
	// doctor 的价值恰恰在于一次跑完给出全景，而不是停在第一个问题上。
	_ = group.Wait()

	slices.SortFunc(results, func(a, b CheckResult) int { return cmp.Compare(a.Name, b.Name) })
	report.Checks = results
	return report
}

// check 是一项待执行的检查。
type check struct {
	name string
	run  func(context.Context) CheckResult
}

// runCheck 以独立超时执行一项检查并记录耗时。
func runCheck(parent context.Context, chk check, timeout time.Duration) CheckResult {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	start := time.Now()
	result := chk.run(ctx)
	result.Name = chk.name
	result.LatencyMS = time.Since(start).Milliseconds()

	// 超时统一归一到固定错误码，便于下游按码聚合。
	if result.Status == StatusFail && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.ErrorCode = CodeTimeout
	}
	return result
}

func buildChecks(cfg *config.Config, opts Options) []check {
	return []check{
		{name: "config", run: func(context.Context) CheckResult { return checkConfig(cfg) }},
		{name: "jwt_keys", run: func(context.Context) CheckResult { return checkJWTKeys(cfg) }},
		{name: "log_dir_writable", run: func(context.Context) CheckResult { return checkLogDir(cfg, opts.Deep) }},
		{name: "pool_consistency", run: func(context.Context) CheckResult { return checkPoolConsistency(cfg) }},
		{name: "database", run: func(ctx context.Context) CheckResult {
			return checkProbe(ctx, opts, opts.Probes.PingDB, true)
		}},
		{name: "redis", run: func(ctx context.Context) CheckResult {
			return checkProbe(ctx, opts, opts.Probes.PingRedis, opts.RedisRequired)
		}},
	}
}

func checkConfig(cfg *config.Config) CheckResult {
	if err := cfg.Validate(); err != nil {
		// 只输出"有多少项不合法"，不回显错误原文：配置校验消息里可能带上
		// 用户名、路径等环境信息，而 doctor 的输出常被直接粘进工单。
		return CheckResult{
			Status: StatusFail, ErrorCode: CodeConfigInvalid,
			Detail: fmt.Sprintf("configuration failed strict validation (%d issue group(s))", countJoined(err)),
		}
	}
	return CheckResult{Status: StatusPass, ErrorCode: CodeOK, Detail: "strict validation passed"}
}

// countJoined 统计 errors.Join 聚合的错误数量。
func countJoined(err error) int {
	// 接口里必须带上 error：errors.AsType 的类型参数约束是 error，
	// 而 errors.As 接受任意接口。go fix 的 errorsastype 改写器在这里会给出
	// 裸 interface{ Unwrap() []error }，那个版本编译不过
	// （"does not satisfy error (missing method Error)"）。
	// 两种写法语义一致：错误链上被遍历的每个元素本身都是 error。
	if joined, ok := errors.AsType[interface {
		error
		Unwrap() []error
	}](err); ok {
		return len(joined.Unwrap())
	}
	return 1
}

// checkJWTKeys 校验密钥文件存在且权限不过宽。
//
// 只看文件元数据，绝不读取内容——doctor 的输出会被粘贴到各种地方，
// 而私钥一旦出现在任何一份日志里就等于泄漏。
// checkJWTKeys 校验**当前生效的**密钥文件。
//
// 曾经读的是 cfg.JWT.PrivateKeyPath / PublicKeyPath——那是密钥轮换落地前的
// 单密钥字段，如今已废弃且恒为空。空路径被循环直接跳过，于是这项检查在
// 密钥完全缺失时照样报 pass：它检查的是一个不再被使用的字段。
//
// **那比不检查更糟**：doctor 的用途正是回答"这份配置能不能起来"，
// 而它给出的是一个虚假的肯定。
func checkJWTKeys(cfg *config.Config) CheckResult {
	paths := []string{cfg.JWT.Active.PrivateKeyPath, cfg.JWT.Active.PublicKeyPath}
	// 退役公钥仍参与验签，缺失会让持有旧令牌的用户在轮换后集体失效。
	for _, previous := range cfg.JWT.Previous {
		paths = append(paths, previous.PublicKeyPath)
	}

	var missing, insecure int
	for _, p := range paths {
		// 空路径算缺失，**不能跳过**。
		//
		// 跳过正是上一版失效的方式：废弃字段恒为空，于是每条路径都被跳过，
		// 计数为零，报告 pass。空的活跃密钥路径意味着服务签不出令牌，
		// 那是缺失，不是"无需检查"。
		if p == "" {
			missing++
			continue
		}
		info, err := os.Stat(resource.ResolvePath(p))
		if err != nil {
			missing++
			continue
		}
		// 权限判定按平台分流（keyperm_unix.go / keyperm_windows.go）：
		// Unix 权限位在 Windows 上没有对应语义，就地写 &0o077 会让该平台恒报过宽。
		// 上面的存在性判定不分平台——它在任何平台上都成立。
		if keyPermissionTooOpen(info) {
			insecure++
		}
	}

	switch {
	case missing > 0:
		return CheckResult{
			Status: StatusFail, ErrorCode: CodeMissing,
			Detail: fmt.Sprintf("%d configured key file(s) are missing or unreadable", missing),
		}
	case insecure > 0:
		return CheckResult{
			Status: StatusFail, ErrorCode: CodeInsecurePerm,
			Detail: fmt.Sprintf("%d key file(s) are readable by group or others; use 0600", insecure),
		}
	default:
		return CheckResult{Status: StatusPass, ErrorCode: CodeOK, Detail: "key files present with tight permissions"}
	}
}

// checkLogDir 校验日志目录可写。deep 关闭时跳过（它要写一个探针文件）。
func checkLogDir(cfg *config.Config, deep bool) CheckResult {
	if !deep {
		return CheckResult{Status: StatusSkipped, ErrorCode: CodeNotRequestedDeep, Detail: "requires --deep"}
	}
	if cfg.Log.Path == "" {
		return CheckResult{Status: StatusSkipped, ErrorCode: CodeMissing, Detail: "log.path is not configured"}
	}

	// 向上找到最近的**已存在**祖先目录再测可写性。
	//
	// 直接测 filepath.Dir(log.Path) 会把"目录还没建"误报成不可写——
	// 日志目录本来就由 logger 在启动时创建。真正要回答的问题是
	// "这个进程能不能在那条路径上创建并写入文件"，而它取决于最近的已存在祖先。
	dir, ok := nearestExistingDir(filepath.Dir(resource.ResolvePath(cfg.Log.Path)))
	if !ok {
		return CheckResult{
			Status: StatusFail, ErrorCode: CodeMissing,
			Detail: "no existing ancestor directory for the configured log path",
		}
	}

	f, err := os.CreateTemp(dir, ".doctor-write-probe-*")
	if err != nil {
		return CheckResult{
			Status: StatusFail, ErrorCode: CodePermission,
			Detail: "log directory is not writable",
		}
	}
	name := f.Name()
	_ = f.Close()
	// 探针文件立即删除：doctor 只读不改，留下的文件本身就是一次修改。
	_ = os.Remove(name)

	return CheckResult{Status: StatusPass, ErrorCode: CodeOK, Detail: "log directory is writable"}
}

// nearestExistingDir 向上寻找最近的已存在目录。
//
// 到达文件系统根仍不存在时返回 false——那说明路径本身有问题，
// 而不是"目录还没建"。
func nearestExistingDir(dir string) (string, bool) {
	for {
		info, err := os.Stat(dir)
		if err == nil && info.IsDir() {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// checkPoolConsistency 做跨字段的交叉校验。
//
// 严格解码只保证"字段名认识"，不保证组合有意义——
// max_idle > max_open 这类配置每个字段单看都合法，组合起来却让连接池行为异常。
func checkPoolConsistency(cfg *config.Config) CheckResult {
	var issues []string

	if cfg.Database.MaxOpenConn > 0 && cfg.Database.MaxIdleConn > cfg.Database.MaxOpenConn {
		issues = append(issues, "database.maxidleconn exceeds database.maxopenconn")
	}
	if cfg.Redis.PoolSize > 0 && cfg.Redis.MinIdleConns > cfg.Redis.PoolSize {
		issues = append(issues, "redis.min_idle_conns exceeds redis.pool_size")
	}

	if len(issues) > 0 {
		slices.Sort(issues)
		return CheckResult{
			Status: StatusFail, ErrorCode: CodeInconsistent,
			Detail: fmt.Sprintf("%d inconsistent pool setting(s): %v", len(issues), issues),
		}
	}
	return CheckResult{Status: StatusPass, ErrorCode: CodeOK, Detail: "pool settings are consistent"}
}

// checkProbe 执行一次注入的连通性探测。
//
// required 为 false 的依赖失败记 degraded 而不是 fail：它不影响进程对外服务，
// 把它记成 fail 会让退出码变成 1，于是"部署前自检"在一台完全健康的机器上失败。
func checkProbe(ctx context.Context, opts Options, probe func(context.Context) error, required bool) CheckResult {
	if !opts.Deep {
		return CheckResult{Status: StatusSkipped, ErrorCode: CodeNotRequestedDeep, Detail: "requires --deep"}
	}
	if probe == nil {
		// 探测能力建立失败与"没接探针"必须区分：前者是 deep 检查要报告的结论本身。
		// 记成 skipped 会让退出码为 0，而使用者加 --deep 要的正是连通性结论。
		if opts.ProbesUnavailable {
			status := StatusDegraded
			if required {
				status = StatusFail
			}
			// 不回显构建错误：它常带完整 DSN。原因由命令层打到 stderr。
			return CheckResult{
				Status: status, ErrorCode: CodeUnreachable,
				Detail: "probe capability could not be established; see stderr for the reason",
			}
		}
		return CheckResult{Status: StatusSkipped, ErrorCode: CodeCapabilityOff, Detail: "no probe wired"}
	}

	if err := probe(ctx); err != nil {
		status := StatusDegraded
		if required {
			status = StatusFail
		}
		// 绝不回显原始错误：它经常带完整 DSN（含用户名，有时含密码）。
		return CheckResult{Status: status, ErrorCode: CodeUnreachable, Detail: "dependency is not reachable"}
	}
	return CheckResult{Status: StatusPass, ErrorCode: CodeOK, Detail: "reachable"}
}

// WriteJSON 把报告以确定性 JSON 写出。
func WriteJSON(w io.Writer, report Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		return fmt.Errorf("encode doctor report: %w", err)
	}
	return nil
}
