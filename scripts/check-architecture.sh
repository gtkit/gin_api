#!/usr/bin/env bash
# check-architecture.sh — enforce the project's documented layering rules.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

# 模块路径从 go.mod 读取，绝不硬编码。
#
# 原因是派生项目：gonew 只重写 .go 文件里的 **import 路径与根包名**（parser.ImportsOnly），
# 不碰 shell 脚本。硬编码母版路径的正则在派生项目里永远匹配不到任何东西，
# 传感器会静默全通过——那比没有传感器更糟，因为它给出的是虚假的合规保证。
MODULE_PATH="$(sed -n -E 's/^module[[:space:]]+([^[:space:]]+).*$/\1/p' go.mod | head -n 1)"
if [ -z "$MODULE_PATH" ]; then
  echo "FAIL: cannot read the module path from go.mod" >&2
  exit 1
fi
# 先限定字符集再转义，而不是试图通用地转义任意字符：
# 合法 module path 只可能含字母、数字与 . / _ - ~，其中唯一的 ERE 元字符是 `.`。
# 限定字符集后只需转义 `.`，既避免了跨平台 sed 字符类差异，也杜绝了把元字符
# 意外带进正则。出现字符集之外的内容说明 go.mod 有异常，直接失败而不是猜。
if ! printf '%s' "$MODULE_PATH" | grep -qE '^[A-Za-z0-9._~/-]+$'; then
  echo "FAIL: unexpected characters in module path: $MODULE_PATH" >&2
  exit 1
fi
MOD="$(printf '%s' "$MODULE_PATH" | sed 's/\./\\./g')"

fail=0

check_no_match() {
  local label="$1"
  local pattern="$2"
  shift 2

  local tmp
  tmp="$(mktemp)"

  for path in "$@"; do
    [ -d "$path" ] || continue
    find "$path" -type f -name '*.go' ! -name '*_test.go' -print0 |
      xargs -0 grep -nE "$pattern" >>"$tmp" 2>/dev/null || true
  done

  if [ -s "$tmp" ]; then
    echo "FAIL: $label"
    cat "$tmp"
    fail=1
  fi

  rm -f "$tmp"
}

check_no_match \
  "application imports gin / gorm / repository" \
  "gorm\.io/gorm|${MOD}/internal/repository/|github\.com/gin-gonic/gin" \
  internal/module/*/application

check_no_match \
  "transport/http imports gorm / repository" \
  "${MOD}/internal/repository/|gorm\.io/gorm" \
  internal/module/*/transport/http

check_no_match \
  "repository imports gin / module" \
  "github\.com/gin-gonic/gin|${MOD}/internal/module/" \
  internal/repository

check_no_match \
  "internal/pkg imports module" \
  "${MOD}/internal/module/" \
  internal/pkg

# pkg/ 是对外发布的可复用包，绝不能依赖仓库内部包。
#
# 这不是风格约束而是硬性限制：pkg 一旦被外部项目引用，而它自身 import 了
# 本仓 internal/，外部项目的编译会因 Go 的 internal 可见性规则直接失败。
# 违反这条的包只是**看起来**可复用——在本仓内怎么跑都正常，
# 只有真正被外部引用时才暴露，而那时已经发布出去了。
check_no_match \
  "pkg imports internal (breaks external consumers)" \
  "${MOD}/internal/" \
  pkg

# pkg/ 不绑定业务日志栈：事件应经回调交给调用方。
#
# 包内直接写日志会把使用者绑定到某个具体日志库，而换一个库只是换个绑定对象。
check_no_match \
  "pkg imports a logging stack (emit events to the caller instead)" \
  'github\.com/gtkit/logger|go\.uber\.org/zap|log/slog' \
  pkg

check_no_match \
  "worker imports gorm / repository" \
  "${MOD}/internal/repository/|gorm\.io/gorm" \
  internal/module/*/worker

check_no_match \
  "application / dto / models import ormx (GORM infrastructure)" \
  'github\.com/gtkit/ormx' \
  internal/module/*/application internal/module/*/dto internal/models

check_no_match \
  "application / dto / models import logger (use port injection)" \
  'github\.com/gtkit/logger' \
  internal/module/*/application internal/module/*/dto internal/models

# 路由只能经中央 registrar 注册。
#
# Gin 的 IRoutes 有十余个注册入口（GET/POST/PUT/PATCH/DELETE/HEAD/OPTIONS/Any/Match
# 与四个 Static*）。任何一个被业务代码直接调用，都会产生一条绕过 RouteSpec 的路由——
# 它没有访问策略、没有超时预算，也不会出现在双向契约测试里，却真实对外服务。
#
# 用"唯一入口白名单"而不是"逐个方法黑名单"：后者是黑名单，框架加一个新方法就漏一个。
# 判据因此是——除 registrar 文件外，全仓生产代码不得出现任何路由注册调用。
#
# NoRoute / NoMethod 显式豁免：它们注册的是 404/405 兜底 handler 而非路由，
# 不进 RouteSpec 集合，双向契约检查也把它们排除。
#
# 判据要求第一个实参是路径字面量（以 " 开头且紧跟 / 或收尾引号）——路由注册的第一个参数
# 总是路径。不这样限定的话，zap.Any("k", v) 之类同名方法会被误报，
# 而一条永远为真的规则最终只会被人加豁免绕过。
ROUTE_REGISTRAR="internal/router/registrar.go"
route_hits="$(mktemp)"
find internal cmd -type f -name '*.go' ! -name '*_test.go' -print0 2>/dev/null |
  xargs -0 grep -nE '\.(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|Any|Match|Handle|Static|StaticFS|StaticFile|StaticFileFS)\("(/|")' 2>/dev/null |
  grep -v '^[^:]*:[0-9]*:[[:space:]]*//' |
  grep -v "^${ROUTE_REGISTRAR}:" |
  grep -v '^internal/router/routespec/pathtree\.go:' >>"$route_hits" || true
if [ -s "$route_hits" ]; then
  echo "FAIL: routes must be registered through ${ROUTE_REGISTRAR} from a RouteSpec, not by calling gin registration methods directly"
  cat "$route_hits"
  fail=1
fi
rm -f "$route_hits"

# 模块 transport 不得自行分组或挂路由级中间件。
#
# 各模块只返回带完整相对路径的 []RouteSpec；前缀拼接与 handler chain 顺序由中央
# registrar 统一负责。模块自己 Group/Use 正是"新增路由忘了挂中间件就静默公开"的成因。
transport_hits="$(mktemp)"
find internal/module -type f -name '*.go' ! -name '*_test.go' -print0 2>/dev/null |
  xargs -0 grep -nE '\.(Group|Use)\(' 2>/dev/null |
  grep -v '^[^:]*:[0-9]*:[[:space:]]*//' >>"$transport_hits" || true
if [ -s "$transport_hits" ]; then
  echo "FAIL: module transport must not call Group/Use; contribute RouteSpec instead"
  cat "$transport_hits"
  fail=1
fi
rm -f "$transport_hits"

# 客户端 IP 只能经唯一 accessor 取值。
#
# 为什么这条规则必须存在：IP 同时是审计事实与限流输入，两处各自裸调 ClientIP() 时，
# 任何一处加了归一化就会让"日志里的 IP"与"被限流的 IP"指向不同主体；
# 收口后，后续给限流 key 做 HMAC、给观测做脱敏也只有一个改动点。
#
# accessor 自身是唯一豁免——它就是那个入口。
#
# 只匹配真实调用，不匹配注释：这条规则的正文本身就要在注释里解释 `c.ClientIP()` 为何危险，
# 若把注释也算作违规，规则就会永远为真而无法归零，最终被人加豁免绕过。
# 判据用「行首到调用之间不出现 //」来排除整行注释与行尾注释里的提及。
CLIENTIP_ACCESSOR="internal/pkg/ginc/clientip.go"
clientip_hits="$(mktemp)"
find internal cmd -type f -name '*.go' ! -name '*_test.go' -print0 2>/dev/null |
  xargs -0 grep -nE '^[^/]*[^/[:space:]]\.ClientIP\(\)' 2>/dev/null |
  grep -v '^[^:]*:[0-9]*:[[:space:]]*//' |
  grep -v "^${CLIENTIP_ACCESSOR}:" >>"$clientip_hits" || true
if [ -s "$clientip_hits" ]; then
  echo "FAIL: client IP must be read through ${CLIENTIP_ACCESSOR} (ginc.ClientIP), not by calling ClientIP() directly"
  cat "$clientip_hits"
  fail=1
fi
rm -f "$clientip_hits"

# 出站 HTTP 调用不得使用共享的全局 client。
#
# http.DefaultClient / http.Get / http.Post / http.Head 用的是同一个进程级 client：
#   - 它的 Timeout 为 0，即**没有任何超时**；一个不响应的下游能永久占住 goroutine；
#   - 它的 Transport 是全局共享的，一个依赖的连接池耗尽会波及所有其他依赖；
#   - 没有任何地方能对单个依赖设置连接超时、响应头超时或调用 CloseIdleConnections。
#
# 每个出站依赖必须持有自己的 *http.Client 与 Transport，超时与连接池所有权
# 落在该依赖的 adapter 上。
#
# 判据只匹配真实调用（行首到调用之间不出现 //），这样规则的正文与注释
# 可以自由提及这些 API，规则本身才能归零而不被豁免绕过。
outbound_hits="$(mktemp)"
find internal cmd -type f -name '*.go' ! -name '*_test.go' -print0 2>/dev/null |
  xargs -0 grep -nE 'http\.(DefaultClient|DefaultTransport)\b|http\.(Get|Post|PostForm|Head)\(' 2>/dev/null |
  grep -v '^[^:]*:[0-9]*:[[:space:]]*//' >>"$outbound_hits" || true
if [ -s "$outbound_hits" ]; then
  echo "FAIL: outbound HTTP must use a per-dependency client with its own timeouts, not the shared global one"
  cat "$outbound_hits"
  fail=1
fi
rm -f "$outbound_hits"

# 每个包装 ResponseWriter 的类型都必须实现 Unwrap()。
#
# http.ResponseController 靠 Unwrap() 递归找到底层连接，才能 SetWriteDeadline。
# 任何一个透明 wrapper 漏掉它，链就在那里断开——之后所有 socket 写 deadline
# 变成 ErrNotSupported，而**没有任何报错**：请求照常处理、日志照常记录，
# 只是超时保护静默失效了。
#
# 判据：嵌入 `gin.ResponseWriter` 的结构体所在文件必须出现
# `Unwrap() http.ResponseWriter`。按文件而不是按类型判定是有意的粗粒度——
# 精确到类型需要 AST，而这条规则要挡的是"新加一个 wrapper 忘了实现"，
# 文件级已经足够，且不会误放过（一个文件里加了新 wrapper 却没加 Unwrap 时仍然命中）。
writer_unwrap_fail=0
while IFS= read -r -d '' file; do
  if grep -qE '^[[:space:]]*gin\.ResponseWriter[[:space:]]*$' "$file" &&
     ! grep -qF 'Unwrap() http.ResponseWriter' "$file"; then
    echo "FAIL: $file embeds gin.ResponseWriter but has no Unwrap() http.ResponseWriter"
    echo "      (a broken unwrap chain makes every socket write deadline silently ineffective)"
    writer_unwrap_fail=1
  fi
done < <(find internal -type f -name '*.go' ! -name '*_test.go' -print0)
if [ "$writer_unwrap_fail" -ne 0 ]; then
  fail=1
fi

# 基础设施连接不得有包级后门。
#
# 旧实现在 platform/data 里放了一个包级 daoDB，配套 DB()/Mdb()/Rdb()/DBClose() 这组
# 无接收者的取值函数。后果是任何包在任何时刻都能拿到连接句柄：
#   - "谁负责关闭"没有唯一答案，关停顺序无从保证；
#   - 测试之间经同一个全局互相污染，且无法并行；
#   - 依赖关系不出现在任何函数签名里，装配图变成隐式的。
#
# 判据：这些包级取值函数不得再出现。连接只经 Resources 的方法取用，
# 由持有者按建立的逆序关闭。
#
# 只匹配**声明**（func 开头且无接收者）而不是调用点：这样规则的正文与注释里
# 可以自由提及这些名字，规则本身才能归零而不被豁免绕过。
dao_backdoor_hits="$(mktemp)"
find internal/platform/data -type f -name '*.go' ! -name '*_test.go' -print0 2>/dev/null |
  xargs -0 grep -nE '^func (DB|Mdb|Rdb|RdbExec|DBClose)\(' 2>/dev/null >>"$dao_backdoor_hits" || true
if [ -s "$dao_backdoor_hits" ]; then
  echo "FAIL: package-level connection accessors are forbidden; take handles from Resources methods instead"
  cat "$dao_backdoor_hits"
  fail=1
fi
rm -f "$dao_backdoor_hits"

while IFS= read -r -d '' file; do
  if grep -qE 'func .*mdbCtx' "$file" && ! grep -q 'dbtx.DB(ctx, r.db)' "$file"; then
    echo "FAIL: $file mdbCtx does not call dbtx.DB(ctx, r.db)"
    fail=1
  fi
done < <(find internal/repository -type f -name '*.go' ! -name '*_test.go' -print0)

if [ "$fail" -ne 0 ]; then
  exit 1
fi

echo "architecture check OK"
