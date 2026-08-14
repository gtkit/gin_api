# scripts

项目质量检查脚本目录。

## install-hooks.sh / check-hooks.sh / git-hooks/

`git-hooks/` 下是随仓库分发的 git 钩子（当前只有 `pre-push`，执行 `make check-ci`）。
`install-hooks.sh` 把它们复制到本仓的 git 钩子目录，`check-hooks.sh` 在临时仓库里
实跑验证安装语义与钩子行为。统一入口：

```bash
make hooks        # 安装（幂等；已有非本仓同名钩子时报错中止，不覆盖）
make check-hooks  # 验证（已接入 make check）
```

跳过门禁：`SKIP_GATE=1 git push`，跳过时钩子在 stderr 留痕。

## check-architecture.sh

检查 application、transport/http、repository、worker、internal/pkg 和 GORM context DB 的依赖边界。

```bash
./scripts/check-architecture.sh
```

## check-capability-inventory.sh

校验 `SCAFFOLD.md` 的预置能力清单是否穷尽。覆盖四处：`internal/pkg/*/`、`pkg/*/`、
`internal/middleware/*.go`、`Config` 结构体的顶层 `mapstructure` 段。

```bash
make check-capability-inventory   # 自检 + 实检（已接入 make check）
./scripts/check-capability-inventory.sh --print-scope   # 打印覆盖范围
./scripts/check-capability-inventory.sh --self-test     # 只跑自检
```

覆盖范围由脚本内的 `SCOPES` 单点定义：`--print-scope` 打印它，扫描循环遍历它，
`--self-test` 也遍历它来为每个 scope 构造一个未登记条目并断言判据失败。三者同源，
因此"声明覆盖 A、实际只扫 B"不会发生。新增 scope 需同时实现 `scope_describe` /
`scope_entries` / `scope_match_kind` / `scope_fixture` 四处，缺一则 `--self-test` 失败。

这是一条**存在性**判据：它只回答"这个名字在清单里出现过吗"，检测不到归类错误或
说明与行为不符。扩大覆盖范围不等于提高检查强度。

## test-coverage.sh / check-coverage.sh

`test-coverage.sh` 运行 race 测试并生成聚合 coverage profile，`check-coverage.sh` 校验最低覆盖率。统一入口：

```bash
make check-coverage
```

当前门禁以 `Makefile` 的 `COVERAGE_MIN` 为单一来源（取值理由见其注释），可通过 `COVERAGE_MIN` 提高但不应降低：

```bash
make check-coverage COVERAGE_MIN=65
```

受限沙箱禁止绑定回环监听端口时，可以显式排除仓库中已确认依赖本地 listener 的测试；正常开发和 CI 不设置此变量，始终运行完整测试：

```bash
ALLOW_NO_LOOPBACK=1 make check-coverage
```

