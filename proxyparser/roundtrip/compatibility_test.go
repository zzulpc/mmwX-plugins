package roundtrip_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"testing"

	proxyparser "github.com/zzulpc/mmwX-plugins/proxyparser"
	"github.com/zzulpc/mmwX-plugins/proxyparser/substore"
	"gopkg.in/yaml.v3"
)

const compatibilityUUID = "11111111-1111-1111-1111-111111111111"

func compatibilityURI(t *testing.T, protocol, network, path, host string) string {
	t.Helper()
	if protocol == "vmess" {
		config := map[string]any{
			"v": "2", "ps": "兼容性", "add": "edge.example.com", "port": "443",
			"id": compatibilityUUID, "aid": "0", "scy": "auto", "tls": "tls",
			"sni": "tls.example.com", "path": path, "host": host,
		}
		if network != "" {
			config["net"] = network
		}
		encoded, err := json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		return "vmess://" + base64.StdEncoding.EncodeToString(encoded)
	}
	params := url.Values{"security": {"tls"}, "sni": {"tls.example.com"}, "type": {network}, "path": {path}, "host": {host}}
	if network == "grpc" {
		params.Set("serviceName", path)
		params.Set("authority", host)
	}
	if network == "tcp" {
		params.Set("headerType", "http")
	}
	auth := compatibilityUUID
	if protocol == "trojan" {
		auth = "password"
	}
	return protocol + "://" + auth + "@edge.example.com:443?" + params.Encode() + "#compatibility"
}

func compatibilityYAML(t *testing.T, protocol, network, options string) map[string]any {
	t.Helper()
	// 真正经过 yaml.v3，避免手写映射掩盖 URI 与订阅入口的容器类型差异。
	content := fmt.Sprintf("name: 兼容性\ntype: %s\nserver: edge.example.com\nport: 443\nuuid: %s\npassword: password\ncipher: auto\nalterId: 0\ntls: true\nsni: tls.example.com\n", protocol, compatibilityUUID)
	if network != "" {
		content += "network: " + network + "\n"
	}
	var node map[string]any
	if err := yaml.Unmarshal([]byte(content+options), &node); err != nil {
		t.Fatal(err)
	}
	return node
}

func compatibilityParse(t *testing.T, uri string) map[string]any {
	t.Helper()
	node, err := proxyparser.Parse(uri)
	if err != nil {
		t.Fatalf("解析 URI 失败: %v", err)
	}
	return node
}

func compatibilityRoundtrip(t *testing.T, node map[string]any) map[string]any {
	t.Helper()
	uri, err := substore.NewURIProducer().ProduceOne(substore.Proxy(node))
	if err != nil || uri == "" {
		t.Fatalf("导出 URI 失败: uri=%q, err=%v", uri, err)
	}
	return compatibilityParse(t, uri)
}

func compatibilitySingbox(t *testing.T, node map[string]any) map[string]any {
	t.Helper()
	output, err := substore.NewSingboxProducer().Produce([]substore.Proxy{substore.Proxy(node)}, "internal", nil)
	if err != nil {
		t.Fatal(err)
	}
	list, ok := output.([]map[string]any)
	if !ok || len(list) != 1 {
		t.Fatalf("节点不应被过滤: %#v", output)
	}
	return list[0]
}

func TestVMessTCP转Singbox双入口(t *testing.T) {
	for _, network := range []string{"tcp", ""} {
		for _, entry := range []string{"URI", "YAML"} {
			t.Run(entry+"/"+network, func(t *testing.T) {
				var node map[string]any
				if entry == "URI" {
					node = compatibilityParse(t, compatibilityURI(t, "vmess", network, "", ""))
				} else {
					node = compatibilityYAML(t, "vmess", network, "")
				}
				for _, candidate := range []map[string]any{node, compatibilityRoundtrip(t, node)} {
					got := compatibilitySingbox(t, candidate)
					assertFields(t, "sing-box TCP", got, map[string]any{
						"type": "vmess", "server": "edge.example.com", "server_port": 443,
						"uuid": compatibilityUUID, "tls.enabled": true,
					})
					if _, exists := got["transport"]; exists {
						t.Errorf("原生 TCP 不应生成额外传输配置: %#v", got)
					}
				}
			})
		}
	}
}

func Test特殊传输路径往返双入口(t *testing.T) {
	transports := []struct {
		name, network, options, field string
	}{
		{"WebSocket", "ws", "ws-opts:\n  path: %s\n", "ws-opts.path"},
		{"gRPC", "grpc", "grpc-opts:\n  grpc-service-name: %s\n", "grpc-opts.grpc-service-name"},
		{"H2", "h2", "h2-opts:\n  path: %s\n", "h2-opts.path"},
		{"TCP_HTTP", "tcp", "headerType: http\nhttp-opts:\n  path: %s\n", "http-opts.path"},
		{"XHTTP", "xhttp", "xhttp-opts:\n  path: %s\n", "xhttp-opts.path"},
	}
	for _, protocol := range []string{"vless", "trojan", "vmess"} {
		for _, transport := range transports {
			if protocol != "vless" && (transport.network == "tcp" || transport.network == "xhttp") {
				continue
			}
			for _, path := range []string{"/proxy+route", "/proxy%2Froute", "/proxy space", "/路径?token=a+b&x=%25"} {
				for _, entry := range []string{"URI", "YAML"} {
					t.Run(protocol+"/"+transport.name+"/"+entry+"/"+path, func(t *testing.T) {
						var node map[string]any
						if entry == "URI" {
							node = compatibilityParse(t, compatibilityURI(t, protocol, transport.network, path, ""))
						} else {
							node = compatibilityYAML(t, protocol, transport.network, fmt.Sprintf(transport.options, strconv.Quote(path)))
						}
						want := map[string]any{transport.field: path}
						assertFields(t, "入口解析", node, want)
						assertFields(t, "跨包往返", compatibilityRoundtrip(t, node), want)
					})
				}
			}
		}
	}
}

func TestTrojanH2保留路径和Host(t *testing.T) {
	const path = "/h2+path%2Fpart"
	const host = "origin.example.com"
	for _, network := range []string{"h2", "http"} {
		for _, entry := range []string{"URI", "YAML"} {
			t.Run(network+"/"+entry, func(t *testing.T) {
				var node map[string]any
				if entry == "URI" {
					node = compatibilityParse(t, compatibilityURI(t, "trojan", network, path, host))
				} else {
					node = compatibilityYAML(t, "trojan", network, fmt.Sprintf("h2-opts:\n  path: [%s]\n  host: [%s]\n", strconv.Quote(path), host))
				}
				assertFields(t, "Trojan H2 往返", compatibilityRoundtrip(t, node), map[string]any{
					"h2-opts.path": path, "h2-opts.host": []string{host},
				})
			})
		}
	}
}

func TestHTTPUpgrade三协议双入口往返(t *testing.T) {
	const path = "/upgrade+path%2Fpart"
	const host = "origin.example.com"
	for _, protocol := range []string{"vless", "trojan", "vmess"} {
		for _, entry := range []string{"URI", "YAML"} {
			t.Run(protocol+"/"+entry, func(t *testing.T) {
				var node map[string]any
				if entry == "URI" {
					node = compatibilityParse(t, compatibilityURI(t, protocol, "httpupgrade", path, host))
				} else {
					node = compatibilityYAML(t, protocol, "ws", fmt.Sprintf("ws-opts:\n  v2ray-http-upgrade: true\n  path: %s\n  headers:\n    Host: %s\n", strconv.Quote(path), host))
				}
				for _, candidate := range []map[string]any{node, compatibilityRoundtrip(t, node)} {
					assertFields(t, "HTTPUpgrade 中间配置", candidate, map[string]any{
						"network": "ws", "ws-opts.v2ray-http-upgrade": true,
						"ws-opts.path": path, "ws-opts.headers.Host": host,
					})
					assertFields(t, "sing-box HTTPUpgrade", compatibilitySingbox(t, candidate), map[string]any{
						"transport.type": "httpupgrade", "transport.path": path, "transport.host": host,
					})
				}
			})
		}
	}
}

func TestSOCKS5IPv6无认证解析往返(t *testing.T) {
	for _, scheme := range []string{"socks5", "socks"} {
		t.Run(scheme, func(t *testing.T) {
			node := compatibilityParse(t, scheme+"://[2001:db8::1]:1080#ipv6")
			want := map[string]any{"type": "socks5", "server": "2001:db8::1", "port": 1080}
			assertFields(t, "IPv6 首次解析", node, want)
			assertFields(t, "IPv6 往返", compatibilityRoundtrip(t, node), want)
			assertFields(t, "sing-box IPv6", compatibilitySingbox(t, node), map[string]any{"server": "2001:db8::1", "server_port": 1080})
		})
	}
	t.Run("YAML", func(t *testing.T) {
		var node map[string]any
		if err := yaml.Unmarshal([]byte("name: ipv6\ntype: socks5\nserver: 2001:db8::1\nport: 1080\n"), &node); err != nil {
			t.Fatal(err)
		}
		assertFields(t, "YAML IPv6 往返", compatibilityRoundtrip(t, node), map[string]any{"server": "2001:db8::1", "port": 1080})
	})
}
