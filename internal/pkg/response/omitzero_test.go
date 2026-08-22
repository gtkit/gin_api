package resp

import (
	"testing"
	"time"
)

// TestRenderingOmitsZeroTimestamps 钉住"零值时间不出现在响应里"这条能力的**机制**。
//
// 为什么这个测试必须存在于渲染路径上而不是只比对 struct tag：
//
//	encoding/json 的 omitempty 对 struct 类型**永不触发**——写了 `omitempty` 的
//	time.Time 字段在零值时照样输出 "0001-01-01T00:00:00Z"。也就是说一个"看起来
//	已经省略零值"的标注可以完全不生效，而没有任何编译或测试信号。
//	omitzero（Go 1.24 起）按 IsZero() 判定，才是真正生效的写法。
//
//	更关键的是**生产构建带 -tags=sonic**：gin 的 JSON 渲染因此不走标准库。
//	只断言标准库行为的话，一个"sonic 不支持 omitzero"的世界会让全部 DTO 的
//	零值时间照常输出，而测试全绿。所以断言必须落在 gin 的渲染结果上——
//	它用的就是本次构建实际链接的那个编码器。
//
// 本测试与 `make check` 的两种跑法配合：不带 tag 覆盖标准库，带 `-tags sonic`
// 覆盖生产编码器（test-coverage.sh 经 GO_BUILD_TAGS 带上它）。
func TestRenderingOmitsZeroTimestamps(t *testing.T) {
	type payload struct {
		ID        int64     `json:"id"`
		CreatedAt time.Time `json:"created_at,omitzero"`
		UpdatedAt time.Time `json:"updated_at,omitzero"`
	}

	t.Run("零值时间被省略", func(t *testing.T) {
		c, rec := newCtx()
		Ok(c, payload{ID: 7})

		data, ok := decode(t, rec)["data"].(map[string]any)
		if !ok {
			t.Fatalf("响应缺少 data 对象: %s", rec.Body.String())
		}
		for _, field := range []string{"created_at", "updated_at"} {
			if v, present := data[field]; present {
				t.Errorf("零值时间字段 %q 仍被输出为 %v —— omitzero 未生效"+
					"（omitempty 对 struct 类型永不触发，若这里退回了 omitempty 就会看到这条）",
					field, v)
			}
		}
	})

	t.Run("非零值时间照常输出", func(t *testing.T) {
		// 反向锚：缺了它，一个"把时间字段整个丢掉"的实现也能让上面的用例通过。
		c, rec := newCtx()
		Ok(c, payload{ID: 7, CreatedAt: time.Unix(1, 0).UTC()})

		data, ok := decode(t, rec)["data"].(map[string]any)
		if !ok {
			t.Fatalf("响应缺少 data 对象: %s", rec.Body.String())
		}
		if _, present := data["created_at"]; !present {
			t.Error("非零值的 created_at 必须出现在响应里")
		}
		if _, present := data["updated_at"]; present {
			t.Error("仍为零值的 updated_at 不该出现")
		}
	})
}
