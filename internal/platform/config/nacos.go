package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gtkit/httpc"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/knadh/koanf/v2"
)

// Nacos 覆盖层的环境变量与默认值（nacos-config-source spec）。
//
// NACOS_ADDR 是唯一的启用开关：未设置或为空时本层完全不生效，
// 进程不发起任何 Nacos 网络请求，配置来源与引入本层之前一致。
const (
	nacosAddrEnv      = "NACOS_ADDR"
	nacosDataIDEnv    = "NACOS_DATA_ID"
	nacosGroupEnv     = "NACOS_GROUP"
	nacosNamespaceEnv = "NACOS_NAMESPACE_ID"
	nacosUsernameEnv  = "NACOS_USERNAME"
	nacosPasswordEnv  = "NACOS_PASSWORD"

	// dataId/group 默认值即本项目约定：dataId 按环境命名，group 用 Nacos 默认组。
	nacosDefaultDataID = "gin_api_local"
	nacosDefaultGroup  = "DEFAULT_GROUP"

	// Nacos 3.x 的 v3 client HTTP API；v1 OpenAPI 已在该版本移除。
	// 登录换回的 token 与配置内容只在启动期内存中短暂存活，不落盘不写日志。
	nacosLoginPath  = "/nacos/v3/auth/user/login"
	nacosConfigPath = "/nacos/v3/client/cs/config"
)

// nacosRequestTimeout 是登录与拉取各自的超时。显式上限保证启动不会无限等待；
// 测试通过替换 nacosClient 注入更短的超时来覆盖超时分支。
const nacosRequestTimeout = 5 * time.Second

// nacosMaxResponseBytes 是单次登录/拉取响应的体积上限，防异常大响应体拖垮启动。
const nacosMaxResponseBytes = 1 << 20

// nacosClient 是访问 Nacos 的统一 HTTP 客户端。与仓库其余出站调用一样走
// gtkit/httpc：登录用 PostForm（表单编码 + JSON 解码响应），拉配置用
// Get + AddQuery。包级变量是为了让测试能整体替换它注入更短超时。
var nacosClient = newNacosClient()

func newNacosClient() *httpc.Client {
	return httpc.New(
		httpc.WithTimeout(nacosRequestTimeout),
		httpc.WithMaxResponseBytes(nacosMaxResponseBytes),
	)
}

// nacosSettings 汇总 NACOS_* 环境变量解析结果。
type nacosSettings struct {
	addrs       []string
	dataID      string
	group       string
	namespaceID string
	username    string
	password    string
}

// loadNacosSettings 解析 NACOS_* 环境变量。NACOS_ADDR 未设置或为空时返回
// (nil, nil) 表示未启用；地址含空项、凭据未成对提供时返回错误。
func loadNacosSettings() (*nacosSettings, error) {
	rawAddr := strings.TrimSpace(os.Getenv(nacosAddrEnv))
	if rawAddr == "" {
		return nil, nil
	}

	var addrs []string
	for _, item := range strings.Split(rawAddr, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, fmt.Errorf("%s contains an empty address: %q", nacosAddrEnv, rawAddr)
		}
		addrs = append(addrs, item)
	}

	username := os.Getenv(nacosUsernameEnv)
	password := os.Getenv(nacosPasswordEnv)
	if (username == "") != (password == "") {
		return nil, fmt.Errorf("%s and %s must be provided together",
			nacosUsernameEnv, nacosPasswordEnv)
	}

	s := &nacosSettings{
		addrs:       addrs,
		dataID:      firstNonEmpty(strings.TrimSpace(os.Getenv(nacosDataIDEnv)), nacosDefaultDataID),
		group:       firstNonEmpty(strings.TrimSpace(os.Getenv(nacosGroupEnv)), nacosDefaultGroup),
		namespaceID: strings.TrimSpace(os.Getenv(nacosNamespaceEnv)),
		username:    username,
		password:    password,
	}
	return s, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// applyNacosOverrides 拉取 Nacos 配置并作为覆盖层合并进 k。
//
// 合并顺序（nacos-config-source / config-env-override spec）：本地文件最低，
// Nacos 覆盖同名键居中，APP_ 环境变量在 decode 里最后加载、优先级不变。
// NACOS_ADDR 未设置时是 no-op；启用后拉取或解析失败一律返回错误，
// 由调用方终止启动——静默回落会让部署者误以为远端配置已生效。
func applyNacosOverrides(k *koanf.Koanf) error {
	s, err := loadNacosSettings()
	if err != nil {
		return err
	}
	if s == nil {
		return nil
	}

	content, err := fetchNacosConfig(s)
	if err != nil {
		return err
	}
	if err := k.Load(rawbytes.Provider([]byte(content)), yaml.Parser()); err != nil {
		// 错误只带定位信息；koanf 的 YAML 解析错误不含配置内容。
		return fmt.Errorf("parse nacos config (dataId %s, group %s): %w", s.dataID, s.group, err)
	}
	return nil
}

// fetchNacosConfig 依次尝试各地址，登录（若配置了凭据）并拉取配置内容，
// 返回首个成功地址的 YAML 文本。全部失败时返回各地址的错误。
//
// 地址是 host:port；用 httpc 时把绝对 URL 直接传给 PostForm/Get，不设
// WithBaseURL——多地址 failover 需要每次拼不同的 host。
func fetchNacosConfig(s *nacosSettings) (string, error) {
	var errs []error
	for _, addr := range s.addrs {
		token := ""
		if s.username != "" {
			var err error
			token, err = nacosLogin(addr, s)
			if err != nil {
				errs = append(errs, fmt.Errorf("address %s: %w", addr, err))
				continue
			}
		}
		content, err := nacosFetchConfig(addr, token, s)
		if err == nil {
			return content, nil
		}
		errs = append(errs, fmt.Errorf("address %s: %w", addr, err))
	}
	// 不会为空：s.addrs 每项都非空（loadNacosSettings 已保证）。
	return "", errors.Join(errs...)
}

// nacosLogin 换取访问 token。凭据不出现在任何错误信息里。
func nacosLogin(addr string, s *nacosSettings) (string, error) {
	form := url.Values{}
	form.Set("username", s.username)
	form.Set("password", s.password)

	// v1 登录接口把 accessToken 放顶层，本版 v3 包在 data 里；两种形态都接受。
	var parsed struct {
		AccessToken string `json:"accessToken"`
		Code        int    `json:"code"`
		Message     string `json:"message"`
		Data        struct {
			AccessToken string `json:"accessToken"`
		} `json:"data"`
	}
	status, err := nacosClient.PostForm(context.Background(),
		"http://"+addr+nacosLoginPath, nil, form, &parsed)
	if err != nil && status == 0 {
		// 传输层失败：没有任何响应，状态码恒为 0。
		return "", fmt.Errorf("login request: %w", err)
	}
	if status != http.StatusOK {
		// 非 200 一律按状态码报错。响应体可能是合法 JSON 也可能不是；无论
		// 能否解码，文案都以状态码为准（对齐原实现，避免泄露响应的不确定性）。
		return "", fmt.Errorf("login failed: HTTP %d", status)
	}
	if err != nil {
		// 200 但响应体解码失败（空体或非法 JSON）。
		return "", fmt.Errorf("decode login response: %w", err)
	}
	if parsed.Code != 0 {
		return "", fmt.Errorf("login rejected: code %d, message %q", parsed.Code, parsed.Message)
	}
	token := firstNonEmpty(parsed.AccessToken, parsed.Data.AccessToken)
	if token == "" {
		return "", errors.New("login response carries no access token")
	}
	return token, nil
}

// nacosFetchConfig 拉取一次配置内容。错误只携带 dataId、group 与失败类别，
// 不携带配置内容、账号或密码（config-env-override spec 的脱敏要求同源）。
func nacosFetchConfig(addr, token string, s *nacosSettings) (string, error) {
	query := url.Values{}
	query.Set("dataId", s.dataID)
	query.Set("groupName", s.group)
	if s.namespaceID != "" {
		query.Set("namespaceId", s.namespaceID)
	}
	if token != "" {
		query.Set("accessToken", token)
	}

	target, err := httpc.AddQuery("http://"+addr+nacosConfigPath, query)
	if err != nil {
		return "", fmt.Errorf("build config request: %w", err)
	}

	// 用 Response[json.RawMessage] 收整包：data.content 有时是裸字符串、有时是
	// {content: ...} 对象（不同 3.x 小版本有差异），必须拿到原始 JSON 再手动拆。
	resp, err := nacosClient.Get[json.RawMessage](context.Background(), target, nil)
	if err != nil && resp.StatusCode == 0 {
		return "", fmt.Errorf("fetch request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusForbidden && s.username == "" {
			return "", fmt.Errorf("config fetch failed: HTTP %d; the server requires "+
				"authentication, set %s and %s", resp.StatusCode, nacosUsernameEnv, nacosPasswordEnv)
		}
		return "", fmt.Errorf("config fetch failed: HTTP %d (dataId %s, group %s)",
			resp.StatusCode, s.dataID, s.group)
	}

	// v3 client API 的成功响应是 {code, message, data}；data 为配置内容。
	var parsed struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(resp.Data, &parsed); err != nil {
		return "", fmt.Errorf("decode config response (dataId %s, group %s): %w", s.dataID, s.group, err)
	}
	if parsed.Code != 0 {
		return "", fmt.Errorf("config fetch rejected: code %d, message %q (dataId %s, group %s)",
			parsed.Code, parsed.Message, s.dataID, s.group)
	}

	// data.content 以纯字符串与 {content: ...} 对象两种形态出现，两种都接受，
	// 其余形态报错且不回显响应体——它承载的正是配置内容。
	var content string
	if err := json.Unmarshal(parsed.Data, &content); err == nil && content != "" {
		return content, nil
	}
	var wrapped struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(parsed.Data, &wrapped); err == nil && wrapped.Content != "" {
		return wrapped.Content, nil
	}
	return "", fmt.Errorf("config response carries no content (dataId %s, group %s)", s.dataID, s.group)
}