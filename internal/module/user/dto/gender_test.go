package dto

import "testing"

// TestNormalizeGenderKeepsTheContractTruthful 钉住枚举承诺由代码兑现。
//
// 契约声明 gender ∈ {0,1,2}。若数据库里的脏数据或将来新增的取值被直接透传，
// 响应就违反了自己的契约，而客户端的 switch 会落到 default 或直接报错。
// 收敛到"未知"是唯一对老客户端安全的处置。
func TestNormalizeGenderKeepsTheContractTruthful(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input int8
		want  Gender
	}{
		{name: "unknown stays unknown", input: 0, want: GenderUnknown},
		{name: "male is preserved", input: 1, want: GenderMale},
		{name: "female is preserved", input: 2, want: GenderFemale},
		{name: "an unrecognized future value falls back", input: 3, want: GenderUnknown},
		{name: "a negative value falls back", input: -1, want: GenderUnknown},
		{name: "the extreme value falls back", input: 127, want: GenderUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := NormalizeGender(tt.input); got != tt.want {
				t.Fatalf("NormalizeGender(%d) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

// TestGenderEnumMatchesTheDeclaredConstants 验证契约声明与常量一致。
//
// 两者分开维护时，新增一个取值却忘了加进枚举声明，契约就会拒绝一个
// 服务端确实会返回的值——而那种不一致只有客户端校验失败时才暴露。
func TestGenderEnumMatchesTheDeclaredConstants(t *testing.T) {
	t.Parallel()

	values := Gender(0).SchemaEnumValues()
	if len(values) != 3 {
		t.Fatalf("enum values = %v, want three", values)
	}

	// 每个声明的常量都必须出现在枚举里。
	for _, want := range []Gender{GenderUnknown, GenderMale, GenderFemale} {
		var found bool
		for _, v := range values {
			if n, ok := v.(int); ok && Gender(n) == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("constant %d is missing from the declared enum %v", want, values)
		}
	}

	// 反过来：枚举里的每个值都必须能被 NormalizeGender 原样保留，
	// 否则契约会声明一个服务端永远不会返回的取值。
	for _, v := range values {
		n, ok := v.(int)
		if !ok {
			t.Fatalf("enum value %v is not an int", v)
		}
		if got := NormalizeGender(int8(n)); int(got) != n {
			t.Fatalf("NormalizeGender(%d) = %d: the contract declares a value the server never returns",
				n, got)
		}
	}
}
