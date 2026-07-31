package application

import "testing"

func TestMaskPhone(t *testing.T) {
	tests := []struct {
		name  string
		phone string
		want  string
	}{
		{name: "empty", phone: "", want: ""},
		{name: "len 7 unchanged", phone: "1234567", want: "1234567"},
		{name: "len 8 masked", phone: "12345678", want: "123****5678"},
		{name: "standard mobile", phone: "13812345678", want: "138****5678"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := maskPhone(tt.phone); got != tt.want {
				t.Errorf("maskPhone(%q) = %q, want %q", tt.phone, got, tt.want)
			}
		})
	}
}
