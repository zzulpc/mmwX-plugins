package main

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSelectProxyCoreJSON(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		expected proxyCore
		wantErr  bool
	}{
		{name: "Snell v6 数字", raw: `{"type":"snell","version":6}`, expected: coreSingBox},
		{name: "Snell v6 字符串", raw: `{"type":"SNELL","version":"6"}`, expected: coreSingBox},
		{name: "Snell v5", raw: `{"type":"snell","version":5}`, expected: coreMihomo},
		{name: "Snell 缺省版本", raw: `{"type":"snell"}`, expected: coreMihomo},
		{name: "其他协议", raw: `{"type":"vmess","version":6}`, expected: coreMihomo},
		{name: "非法 Snell 版本", raw: `{"type":"snell","version":"six"}`, wantErr: true},
		{name: "非法 JSON", raw: `{`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, err := selectProxyCoreJSON(test.raw)
			if test.wantErr {
				if err == nil {
					t.Fatalf("预期返回错误，实际内核为 %s", actual)
				}
				return
			}
			if err != nil {
				t.Fatalf("选择内核失败: %v", err)
			}
			if actual != test.expected {
				t.Fatalf("内核不符: 实际 %s，预期 %s", actual, test.expected)
			}
		})
	}
}

func TestIntegerValueRejectsFraction(t *testing.T) {
	if _, err := integerValue(6.5); err == nil {
		t.Fatal("小数不应被接受为整数")
	}
}

func TestIntegerValueAcceptsIntegralJSONNumbers(t *testing.T) {
	tests := map[json.Number]int{"6": 6, "6.0": 6, "6e0": 6, "443.000": 443}
	for value, expected := range tests {
		actual, err := integerValue(value)
		if err != nil {
			t.Fatalf("整数形式 %q 应被接受: %v", value, err)
		}
		if actual != expected {
			t.Fatalf("整数形式 %q 转换结果错误: 实际 %d，预期 %d", value, actual, expected)
		}
	}
}

func TestNormalizeJSONNumbersRecursively(t *testing.T) {
	input := map[string]any{
		"port": json.Number("443"),
		"plugin-opts": map[string]any{
			"ratio": json.Number("1.5"),
			"ports": []any{json.Number("80"), json.Number("443")},
		},
	}
	actual := normalizeJSONNumbers(input).(map[string]any)
	if _, ok := actual["port"].(int64); !ok {
		t.Fatalf("整数没有恢复为 int64: %#v", actual["port"])
	}
	pluginOptions := actual["plugin-opts"].(map[string]any)
	if _, ok := pluginOptions["ratio"].(float64); !ok {
		t.Fatalf("小数没有恢复为 float64: %#v", pluginOptions["ratio"])
	}
	ports := pluginOptions["ports"].([]any)
	if _, ok := ports[0].(int64); !ok {
		t.Fatalf("嵌套数组整数没有恢复: %#v", ports[0])
	}
}

func TestBuildMihomoConfigKeepsNumericTypes(t *testing.T) {
	proxy, err := parseClashProxy(`{"name":"回归节点","type":"vmess","server":"127.0.0.1","port":443,"uuid":"00000000-0000-0000-0000-000000000000","alterId":0,"plugin-opts":{"ratio":1.5}}`)
	if err != nil {
		t.Fatalf("解析回归节点失败: %v", err)
	}
	content, err := buildMihomoConfig(proxy, "回归节点", 19006)
	if err != nil {
		t.Fatalf("生成 Mihomo 配置失败: %v", err)
	}
	var decoded map[string]any
	if err := yaml.Unmarshal(content, &decoded); err != nil {
		t.Fatalf("解析 Mihomo YAML 失败: %v", err)
	}
	proxies := decoded["proxies"].([]any)
	node := proxies[0].(map[string]any)
	if _, isString := node["port"].(string); isString {
		t.Fatalf("Mihomo port 被错误写成字符串: %#v", node["port"])
	}
	pluginOptions := node["plugin-opts"].(map[string]any)
	if _, isString := pluginOptions["ratio"].(string); isString {
		t.Fatalf("嵌套小数被错误写成字符串: %#v", pluginOptions["ratio"])
	}
}
