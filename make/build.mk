clean: ## 仅清理默认受管二进制与覆盖率文件，拒绝越界和symlink
	go run ./scripts/clean.go --bin-dir "$(BIN_DIR)" --app-name "$(APP_NAME)" --coverage-profile "$(COVERAGE_PROFILE)"

## 构建mac版本,需要安装upx
# 多个 tag 的正确写法: 多个 tag 之间用逗号或空格分隔
#go build -tags=sonic,debug ./...
#go build -tags="sonic debug" ./...

## 构建linux版本,需要安装upx
build:
	@mkdir -p $(BIN_DIR)
	$(Linux) go build $(BUILD_FLAGS) -o $(BIN_DIR)/$(APP_NAME) . && upx -9 $(BIN_DIR)/$(APP_NAME)

image: ## Build the Docker image locally, tagged with the current commit short sha (requires Docker; servers do not run Docker)
	## APP_NAME 只决定 builder 阶段的产物名；容器内路径固定为 /app/bin/server（exec-form
	## ENTRYPOINT 不做变量展开，退回 shell form 会让 shell 成为 PID 1 并破坏优雅关停）。
	## GOPROXY 透传本机设置：容器内默认 proxy 在部分网络不可达，回退 direct 又需要
	## builder 镜像里并不存在的 git。
	docker build \
		--build-arg APP_NAME=$(APP_NAME) \
		--build-arg BUILD_COMMIT="$(BUILD_COMMIT)" \
		--build-arg BUILD_TIME="$(BUILD_TIME)" \
		--build-arg GOPROXY=$$(go env GOPROXY) \
		-t $(APP_NAME):$$(git rev-parse --short HEAD) .
