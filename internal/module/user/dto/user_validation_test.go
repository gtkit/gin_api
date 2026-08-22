package dto

import (
	"strings"
	"testing"

	"github.com/gtkit/verify"
)

// TestUserUpdateRequestAvatarContract 枚举 Avatar 的输入域。
//
// 两条约束各有独立的失效形态，因此必须分别有用例：
//
//   - **长度**：契约放行一个 `users.avatar`（varchar(255)）存不下的值时，请求会一路
//     走到写库才失败。严格模式下那是一个未被错误映射覆盖的驱动错误，于是一次纯
//     客户端输入错误被记成 500 与 error 级日志，且可被脚本反复触发以污染错误率告警。
//   - **scheme**：通用 `url` 校验**不限 scheme**（实测接受 javascript:／data:／file:／
//     vbscript:）。头像 URL 入库后由任意消费端渲染成链接即是存储型 XSS 载体，
//     而入口是唯一能以零成本拒绝它的位置。
func TestUserUpdateRequestAvatarContract(t *testing.T) {
	if err := verify.New(); err != nil {
		t.Fatalf("verify.New() = %v, want nil", err)
	}

	// avatarColumnWidth 是 users.avatar 的列宽，契约上界必须与它对齐。
	const avatarColumnWidth = 255

	longHost := strings.Repeat("a", 240)

	tests := []struct {
		name    string
		avatar  string
		wantErr bool
	}{
		{name: "https 通过", avatar: "https://example.com/a.png"},
		{name: "http 通过", avatar: "http://example.com/a.png"},
		{name: "空串按未传处理", avatar: ""},
		{
			name:    "恰好等于列宽",
			avatar:  "https://example.com/" + strings.Repeat("b", avatarColumnWidth-len("https://example.com/")),
			wantErr: false,
		},
		{
			name:    "超出列宽一个字符即拒",
			avatar:  "https://example.com/" + strings.Repeat("b", avatarColumnWidth-len("https://example.com/")+1),
			wantErr: true,
		},
		{
			name:    "远超列宽的合法 URL",
			avatar:  "https://" + longHost + ".example.com/" + strings.Repeat("c", 200),
			wantErr: true,
		},
		{name: "javascript scheme", avatar: "javascript:alert(1)", wantErr: true},
		{name: "data scheme", avatar: "data:text/html;base64,PHNjcmlwdD4=", wantErr: true},
		{name: "file scheme", avatar: "file:///etc/passwd", wantErr: true},
		{name: "vbscript scheme", avatar: "vbscript:msgbox(1)", wantErr: true},
		{name: "非 URL 文本", avatar: "not a url", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := verify.Struct(UserUpdateRequest{Avatar: tt.avatar})
			if tt.wantErr && err == nil {
				t.Fatalf("avatar %q 必须被拒（长度 %d）", tt.avatar, len(tt.avatar))
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("avatar %q 必须通过（长度 %d），得到 %v", tt.avatar, len(tt.avatar), err)
			}
		})
	}
}
