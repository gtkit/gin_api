package appmeta

const (
	// APIBasePath 是所有 HTTP API 的根前缀。
	APIBasePath = "/api"
	// APIV1Path 是当前对外暴露的 v1 API 前缀。
	APIV1Path = APIBasePath + "/v1"
	// APIV2Path 是未来 v2 API 的路由前缀。
	APIV2Path = APIBasePath + "/v2"
)
