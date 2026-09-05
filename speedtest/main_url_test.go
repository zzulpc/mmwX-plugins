package main

import (
	"net/url"
	"strings"
	"testing"
)

func TestBuildWSURL(t *testing.T) {
	const token = "pair-token-1234"
	tests := []struct {
		name   string
		master string
		want   string
	}{
		{"https 转 wss", "https://x.example.net", "wss://x.example.net/api/speedtest/tester/ws?token=" + token},
		{"http 转 ws", "http://x.example.net", "ws://x.example.net/api/speedtest/tester/ws?token=" + token},
		{"已是 wss 保持不变", "wss://x.example.net", "wss://x.example.net/api/speedtest/tester/ws?token=" + token},
		{"去掉结尾斜杠", "https://x.example.net/", "wss://x.example.net/api/speedtest/tester/ws?token=" + token},
		{"带端口", "https://x.example.net:8443", "wss://x.example.net:8443/api/speedtest/tester/ws?token=" + token},
		{"覆盖原有路径", "https://x.example.net/mmwx", "wss://x.example.net/api/speedtest/tester/ws?token=" + token},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := buildWSURL(test.master, token)
			if err != nil {
				t.Fatalf("buildWSURL(%q) 报错: %v", test.master, err)
			}
			if got != test.want {
				t.Fatalf("buildWSURL(%q)\n实际 %q\n预期 %q", test.master, got, test.want)
			}
		})
	}
}

func TestBuildWSURL拒绝缺少或不支持的协议头(t *testing.T) {
	// 缺少协议头、协议拼错或使用其它协议过去都可能绕到拨号阶段，
	// 最后只报一句和根因无关的域名或握手错误。
	for _, master := range []string{
		"x.example.net",
		"example.net:8080",
		"//x.example.net",
		"ftp://x.example.net",
		"htps://x.example.net",
		"https://",
		"",
	} {
		t.Run(master, func(t *testing.T) {
			got, err := buildWSURL(master, "pair-token-1234")
			if err == nil {
				t.Fatalf("缺少或不支持协议头的 %q 应当报错，实际返回 %q", master, got)
			}
			if !strings.Contains(err.Error(), "http://") || !strings.Contains(err.Error(), "wss://") {
				t.Fatalf("错误信息应当列出允许的协议头，实际为 %v", err)
			}
		})
	}
}

// maskedURL 是配对令牌不进日志的唯一一道保护，此前覆盖率为 0%。
func TestMaskedURL不泄露配对令牌(t *testing.T) {
	tests := []struct {
		name       string
		token      string
		wantMasked string
	}{
		{"长令牌保留首尾便于对账", "abcdefgh12345678", "abcd…5678"},
		{"刚好八位整体隐藏", "abcdefgh", "***"},
		{"短令牌整体隐藏", "abc", "***"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := "wss://x.example.net/api/speedtest/tester/ws?token=" + test.token
			masked := maskedURL(raw)
			if strings.Contains(masked, test.token) {
				t.Fatalf("脱敏后仍包含完整令牌: %q", masked)
			}
			parsed, err := url.Parse(masked)
			if err != nil {
				t.Fatalf("脱敏结果不是合法 URL: %q (%v)", masked, err)
			}
			if got := parsed.Query().Get("token"); got != test.wantMasked {
				t.Fatalf("脱敏后的 token = %q，预期 %q", got, test.wantMasked)
			}
			if parsed.Host != "x.example.net" || parsed.Path != "/api/speedtest/tester/ws" {
				t.Fatalf("脱敏不应改动主机与路径: %q", masked)
			}
		})
	}
}

func TestMaskedURL无令牌与非法输入原样返回(t *testing.T) {
	plain := "wss://x.example.net/api/speedtest/tester/ws"
	if got := maskedURL(plain); got != plain {
		t.Fatalf("无令牌的地址不应被改动: %q", got)
	}
	// url.Parse 失败时只能原样返回 —— 这条路径上没有令牌可以泄露，但不能崩。
	broken := "https://x.example.net/%zz"
	if got := maskedURL(broken); got != broken {
		t.Fatalf("无法解析的地址应原样返回: %q", got)
	}
}
