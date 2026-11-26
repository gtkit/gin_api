# 应用身份的唯一来源。Make 读取本文件，Go 将它嵌入二进制。
# 改名后重新构建。APP_NAME 同时决定宿主机构建产物名 bin/$(APP_NAME)。
# 保持 KEY = value 格式，每个键出现一次。部署脚本的产物名约定需另行确认。
APP_NAME = gin_api
APP_DISPLAY_NAME = Gin API
