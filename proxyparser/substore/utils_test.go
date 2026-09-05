package substore

import "testing"

func TestGetMap支持字符串映射(t *testing.T) {
	input := map[string]interface{}{
		"headers": map[string]string{
			"Host":       "cdn.example",
			"User-Agent": "mmwx-test",
		},
	}

	got := GetMap(input, "headers")
	if got == nil {
		t.Fatal("GetMap 对 map[string]string 返回了 nil")
	}
	if got["Host"] != "cdn.example" {
		t.Fatalf("Host = %v，期望 cdn.example", got["Host"])
	}
	if got["User-Agent"] != "mmwx-test" {
		t.Fatalf("User-Agent = %v，期望 mmwx-test", got["User-Agent"])
	}
}
