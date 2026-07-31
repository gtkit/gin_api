# ============================================================================
# 构建阶段：golang:1.26 + 静态编译
# 依赖层缓存：先 COPY go.mod/go.sum 下载依赖，源码变更不重复拉依赖。
# ============================================================================
FROM golang:1.26-alpine AS builder

# APP_NAME 决定 builder 阶段的产物名，来源是 project.mk（应用身份的唯一来源），
# 由 `make image` 经 --build-arg 传入。它只在本阶段生效——容器内路径固定，见 stage 2。
ARG APP_NAME
# GOPROXY 可覆盖：容器内默认 proxy.golang.org 在部分网络不可达，Go 会回退 direct，
# 而 direct 需要 builder 基础镜像里并不存在的 git，最终表现为 `go mod download` 失败。
# 依赖全部公开，注入一个可达的 proxy 即可，无需给镜像装 git。
ARG GOPROXY
ENV GOPROXY=${GOPROXY}

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# 必须显式断言 APP_NAME 非空，不能指望 `-o /out/${APP_NAME}` 在变量为空时自然失败：
# 实测 `go build -o out/ .`（即变量为空时的展开结果）exit=0——Go 把结尾为 `/` 的路径
# 当目录处理并生成默认名二进制。少了这条断言，"参数为空则构建失败" 就是假门禁。
RUN test -n "$APP_NAME" || { echo "APP_NAME build-arg is required (see project.mk)" >&2; exit 1; }

# CGO_ENABLED=0：纯 Go 依赖（gorm mysql 驱动无 cgo），产出静态二进制；
# -trimpath 去除构建机路径，-s -w 去符号表减小体积。
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o "/out/${APP_NAME}" .

# ============================================================================
# 运行阶段：alpine + tzdata（application.timezone 需要）+ CA 证书（HTTPS 出站）
#
# 目录布局对齐 resource.WriteRoot 的锚定规则：
#   /app/bin/server 与 /app/config/ 同级（可执行文件上级含 config/ 标志物），
#   同时显式 APP_ROOT=/app，双保险。
# 真实配置不进镜像，运行时挂载：
#   -v <RootPath>/config/env/env.yml:/app/config/env/env.yml:ro
#   -v <RootPath>/config/pem:/app/config/pem:ro
#   -v <RootPath>/logs:/app/logs
# ============================================================================
FROM alpine:3.22

RUN apk add --no-cache tzdata ca-certificates \
    && addgroup -S app && adduser -S -G app app

WORKDIR /app

# 容器内路径固定为 /app/bin/server，与 APP_NAME 解耦。
# 原因：ENTRYPOINT 必须是 exec form（shell form 会让 shell 成为 PID 1，破坏信号转发与
# 僵尸回收，进而破坏优雅关停），而 exec form 不做 ARG/ENV 变量展开——路径无法跟随 APP_NAME。
# 宿主机侧的可执行文件路径由外部冻结的部署脚本决定，与容器内路径互不牵连。
ARG APP_NAME
COPY --from=builder /out/${APP_NAME} /app/bin/server
# 只带入配置参照模板与说明，真实 env.yml/pem 由部署挂载（.dockerignore 已兜底排除）。
COPY --from=builder /src/config/env.yml.example /app/config/env.yml.example
COPY --from=builder /src/config/README.md /app/config/README.md
# 迁移文件必须进镜像：migratex 从文件系统读取 database/migrations，
# 镜像内没有它就跑不了 migrate up。
COPY --from=builder /src/database/migrations /app/database/migrations

RUN mkdir -p /app/config/env /app/config/pem /app/logs \
    && chown -R app:app /app

ENV APP_ROOT=/app

USER app

EXPOSE 8008

ENTRYPOINT ["/app/bin/server"]
CMD ["server"]
