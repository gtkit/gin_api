# ============================================================================
# 构建阶段：golang:1.26 + 静态编译
# 依赖层缓存：先 COPY go.mod/go.sum 下载依赖，源码变更不重复拉依赖。
# ============================================================================
FROM golang:1.26-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0：纯 Go 依赖（gorm mysql 驱动无 cgo），产出静态二进制；
# -trimpath 去除构建机路径，-s -w 去符号表减小体积。
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/app_client .

# ============================================================================
# 运行阶段：alpine + tzdata（application.timezone 需要）+ CA 证书（HTTPS 出站）
#
# 目录布局对齐 resource.WriteRoot 的锚定规则：
#   /app/bin/app_client 与 /app/config/ 同级（可执行文件上级含 config/ 标志物），
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

COPY --from=builder /out/app_client /app/bin/app_client
# 只带入配置参照模板与说明，真实 env.yml/pem 由部署挂载（.dockerignore 已兜底排除）。
COPY --from=builder /src/config/env.yml.example /app/config/env.yml.example
COPY --from=builder /src/config/README.md /app/config/README.md

RUN mkdir -p /app/config/env /app/config/pem /app/logs \
    && chown -R app:app /app

ENV APP_ROOT=/app

USER app

EXPOSE 8008

ENTRYPOINT ["/app/bin/app_client"]
CMD ["server"]
