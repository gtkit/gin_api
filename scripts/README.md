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

