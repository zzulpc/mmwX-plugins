package valueutil

import "testing"

func TestTruthy(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  bool
	}{
		{name: "布尔真", value: true, want: true},
		{name: "布尔假", value: false, want: false},
		{name: "带空格的大写真", value: " TRUE ", want: true},
		{name: "数字字符串真", value: "1", want: true},
		{name: "yes", value: "yes", want: true},
		{name: "on", value: "on", want: true},
		{name: "false", value: "false", want: false},
		{name: "零字符串", value: "0", want: false},
		{name: "任意文本", value: "abc", want: false},
		{name: "空白", value: "  ", want: false},
		{name: "整数非零", value: int64(-2), want: true},
		{name: "整数零", value: uint32(0), want: false},
		{name: "浮点非零", value: 0.5, want: true},
		{name: "浮点零", value: float32(0), want: false},
		{name: "未知类型", value: []string{"true"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Truthy(tt.value); got != tt.want {
				t.Fatalf("Truthy(%#v) = %v，期望 %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestFirstString(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "空值", value: nil, want: ""},
		{name: "字符串", value: "host.example.com", want: "host.example.com"},
		{name: "字符串数组跳过空值", value: []string{"", "second"}, want: "second"},
		{name: "混合数组跳过非字符串", value: []any{42, "", "second"}, want: "second"},
		{name: "嵌套数组", value: []any{[]string{"", "nested"}}, want: "nested"},
		{name: "数字不转文本", value: 42, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FirstString(tt.value); got != tt.want {
				t.Fatalf("FirstString(%#v) = %q，期望 %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestIP分类(t *testing.T) {
	tests := []struct {
		value string
		ipv4  bool
		ipv6  bool
	}{
		{value: "192.0.2.1", ipv4: true},
		{value: "2001:db8::1", ipv6: true},
		{value: "::", ipv6: true},
		{value: "::ffff:192.0.2.1", ipv4: true},
		{value: ":::::::", ipv4: false, ipv6: false},
		{value: "1:2:3", ipv4: false, ipv6: false},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := IsIPv4(tt.value); got != tt.ipv4 {
				t.Errorf("IsIPv4(%q) = %v，期望 %v", tt.value, got, tt.ipv4)
			}
			if got := IsIPv6(tt.value); got != tt.ipv6 {
				t.Errorf("IsIPv6(%q) = %v，期望 %v", tt.value, got, tt.ipv6)
			}
		})
	}
}
