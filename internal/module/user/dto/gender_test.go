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
