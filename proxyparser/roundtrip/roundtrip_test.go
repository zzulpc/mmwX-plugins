package roundtrip_test

import (
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"testing"

	proxyparser "github.com/zzulpc/mmwX-plugins/proxyparser"
	"github.com/zzulpc/mmwX-plugins/proxyparser/substore"
)

type roundtripCase struct {
	name string
	uri  string
	want map[string]any
}

func Test代理URI跨包往返(t *testing.T) {
	cases := []roundtripCase{
		{
			name: "VLESS WebSocket CDN 回源",
			uri:  "vless://11111111-1111-1111-1111-111111111111@edge.example.com:443?type=ws&security=tls&sni=tls-ws.example.com&host=origin-ws.example.com&path=%2Fvless-ws&alpn=h2&fp=chrome#vless-ws",
			want: map[string]any{
				"name": "vless-ws", "type": "vless", "server": "edge.example.com", "port": 443,
				"uuid": "11111111-1111-1111-1111-111111111111", "network": "ws", "tls": true,
				"servername": "tls-ws.example.com", "client-fingerprint": "chrome", "alpn": []string{"h2"},
				"ws-opts.path": "/vless-ws", "ws-opts.headers.Host": "origin-ws.example.com",
			},
		},
		{
			name: "VLESS gRPC CDN 回源",
			uri:  "vless://11111111-1111-1111-1111-111111111112@edge.example.com:443?type=grpc&security=tls&sni=tls-grpc.example.com&authority=origin-grpc.example.com&serviceName=svc&mode=gun&fp=chrome#vless-grpc",
			want: map[string]any{
				"name": "vless-grpc", "type": "vless", "server": "edge.example.com", "port": 443,
				"uuid": "11111111-1111-1111-1111-111111111112", "network": "grpc", "tls": true,
				"servername": "tls-grpc.example.com", "client-fingerprint": "chrome",
				"grpc-opts.grpc-service-name": "svc", "grpc-opts._grpc-authority": "origin-grpc.example.com",
				"grpc-opts._grpc-type": "gun",
			},
		},
		{
			name: "VLESS gRPC Host 回退",
			uri:  "vless://11111111-1111-1111-1111-111111111115@edge.example.com:443?type=grpc&security=tls&sni=tls-grpc-host.example.com&host=origin-grpc-host.example.com&serviceName=host-fallback-svc&mode=gun#vless-grpc-host",
			want: map[string]any{
				"name": "vless-grpc-host", "type": "vless", "server": "edge.example.com", "port": 443,
				"uuid": "11111111-1111-1111-1111-111111111115", "network": "grpc", "tls": true,
				"servername": "tls-grpc-host.example.com", "grpc-opts.grpc-service-name": "host-fallback-svc",
				"grpc-opts._grpc-authority": "origin-grpc-host.example.com", "grpc-opts._grpc-type": "gun",
			},
		},
		{
			name: "VLESS H2",
			uri:  "vless://11111111-1111-1111-1111-111111111113@edge.example.com:443?type=h2&security=tls&sni=tls-h2.example.com&host=origin-h2.example.com&path=%2Fvless-h2#vless-h2",
			want: map[string]any{
				"name": "vless-h2", "type": "vless", "server": "edge.example.com", "port": 443,
				"uuid": "11111111-1111-1111-1111-111111111113", "network": "h2", "tls": true,
				"servername": "tls-h2.example.com", "h2-opts.path": "/vless-h2",
				"h2-opts.host": []string{"origin-h2.example.com"},
			},
		},
		{
			name: "VLESS TCP",
			uri:  "vless://11111111-1111-1111-1111-111111111114@tcp.example.com:443?type=tcp&security=tls&sni=tls-tcp.example.com&flow=xtls-rprx-vision&alpn=h2&fp=chrome#vless-tcp",
			want: map[string]any{
				"name": "vless-tcp", "type": "vless", "server": "tcp.example.com", "port": 443,
				"uuid": "11111111-1111-1111-1111-111111111114", "network": "tcp", "tls": true,
				"servername": "tls-tcp.example.com", "flow": "xtls-rprx-vision",
				"client-fingerprint": "chrome", "alpn": []string{"h2"},
			},
		},
		{
			name: "VLESS TCP HTTP 伪装",
			uri:  "vless://11111111-1111-1111-1111-111111111116@edge.example.com:443?type=tcp&headerType=http&security=tls&sni=tls-tcp-http.example.com&host=origin-tcp-http.example.com&path=%2Ftcp-http#vless-tcp-http",
			want: map[string]any{
				"name": "vless-tcp-http", "type": "vless", "server": "edge.example.com", "port": 443,
				"uuid": "11111111-1111-1111-1111-111111111116", "network": "tcp", "tls": true,
				"servername": "tls-tcp-http.example.com", "headerType": "http",
				"http-opts.path": "/tcp-http", "http-opts.headers.Host": "origin-tcp-http.example.com",
			},
		},
		{
			name: "VMess WebSocket CDN 回源",
			uri:  "vmess://eyJ2IjoiMiIsInBzIjoidm1lc3Mtd3MiLCJhZGQiOiJlZGdlLmV4YW1wbGUuY29tIiwicG9ydCI6IjQ0MyIsImlkIjoiMjIyMjIyMjItMjIyMi0yMjIyLTIyMjItMjIyMjIyMjIyMjIyIiwiYWlkIjoiMCIsInNjeSI6ImF1dG8iLCJuZXQiOiJ3cyIsInR5cGUiOiJub25lIiwiaG9zdCI6Im9yaWdpbi12bWVzcy5leGFtcGxlLmNvbSIsInBhdGgiOiIvdm1lc3MiLCJ0bHMiOiJ0bHMiLCJzbmkiOiJ0bHMtdm1lc3MuZXhhbXBsZS5jb20iLCJhbHBuIjoiaDIiLCJmcCI6ImNocm9tZSJ9",
			want: map[string]any{
				"name": "vmess-ws", "type": "vmess", "server": "edge.example.com", "port": 443,
				"uuid": "22222222-2222-2222-2222-222222222222", "alterId": 0, "cipher": "auto",
				"network": "ws", "tls": true, "servername": "tls-vmess.example.com",
				"client-fingerprint": "chrome", "alpn": []string{"h2"},
				"ws-opts.path": "/vmess", "ws-opts.headers.Host": "origin-vmess.example.com",
			},
		},
		{
			name: "VMess gRPC Host 回读",
			uri:  "vmess://eyJ2IjoiMiIsInBzIjoidm1lc3MtZ3JwYyIsImFkZCI6ImdycGMtdm1lc3MuZXhhbXBsZS5jb20iLCJwb3J0IjoiNDQzIiwiaWQiOiIyMjIyMjIyMi0yMjIyLTIyMjItMjIyMi0yMjIyMjIyMjIyMjMiLCJhaWQiOiIwIiwic2N5IjoiYXV0byIsIm5ldCI6ImdycGMiLCJ0eXBlIjoiZ3VuIiwiaG9zdCI6Im9yaWdpbi12bWVzcy1ncnBjLmV4YW1wbGUuY29tIiwicGF0aCI6InZtZXNzLXN2YyIsInRscyI6InRscyIsInNuaSI6InRscy12bWVzcy1ncnBjLmV4YW1wbGUuY29tIn0=",
			want: map[string]any{
				"name": "vmess-grpc", "type": "vmess", "server": "grpc-vmess.example.com", "port": 443,
				"uuid": "22222222-2222-2222-2222-222222222223", "alterId": 0, "cipher": "auto",
				"network": "grpc", "tls": true, "servername": "tls-vmess-grpc.example.com",
				"grpc-opts.grpc-service-name": "vmess-svc",
				"grpc-opts._grpc-authority":   "origin-vmess-grpc.example.com",
			},
		},
		{
			name: "Trojan WebSocket",
			uri:  "trojan://password@edge.example.com:443?type=ws&sni=tls-trojan.example.com&host=origin-trojan.example.com&path=%2Ftrojan-ws&alpn=h2&fp=chrome#trojan-ws",
			want: map[string]any{
				"name": "trojan-ws", "type": "trojan", "server": "edge.example.com", "port": 443,
				"password": "password", "network": "ws", "tls": true, "sni": "tls-trojan.example.com",
				"client-fingerprint": "chrome", "alpn": []string{"h2"},
				"ws-opts.path": "/trojan-ws", "ws-opts.headers.Host": "origin-trojan.example.com",
			},
		},
		{
			name: "Trojan gRPC Authority 回读",
			uri:  "trojan://password@grpc-trojan.example.com:443?type=grpc&sni=tls-trojan-grpc.example.com&authority=origin-trojan-grpc.example.com&serviceName=trojan-svc#trojan-grpc",
			want: map[string]any{
				"name": "trojan-grpc", "type": "trojan", "server": "grpc-trojan.example.com", "port": 443,
				"password": "password", "network": "grpc", "tls": true, "sni": "tls-trojan-grpc.example.com",
				"grpc-opts.grpc-service-name": "trojan-svc",
				"grpc-opts._grpc-authority":   "origin-trojan-grpc.example.com",
			},
		},
		{
			name: "Trojan TCP",
			uri:  "trojan://password@tcp.example.com:443?type=tcp&sni=tls-trojan-tcp.example.com&alpn=h2&fp=chrome#trojan-tcp",
			want: map[string]any{
				"name": "trojan-tcp", "type": "trojan", "server": "tcp.example.com", "port": 443,
				"password": "password", "network": "tcp", "tls": true, "sni": "tls-trojan-tcp.example.com",
				"client-fingerprint": "chrome", "alpn": []string{"h2"},
			},
		},
		{
			name: "Shadowsocks",
			uri:  "ss://YWVzLTI1Ni1nY206cGFzcw==@198.51.100.10:8388#ss",
			want: map[string]any{
				"name": "ss", "type": "ss", "server": "198.51.100.10", "port": 8388,
				"cipher": "aes-256-gcm", "password": "pass", "udp": true,
			},
		},
		{
			name: "Hysteria2",
			uri:  "hysteria2://pass@hy2.example.com:443?sni=tls-hy2.example.com&insecure=1&obfs=salamander&obfs-password=secret&mport=20000-30000&hop-interval=30&pinSHA256=ABCDEF#hy2",
			want: map[string]any{
				"name": "hy2", "type": "hysteria2", "server": "hy2.example.com", "port": 443,
				"password": "pass", "sni": "tls-hy2.example.com", "skip-cert-verify": true,
				"obfs": "salamander", "obfs-password": "secret", "ports": "20000-30000",
				"hop-interval": 30, "tls-fingerprint": "ABCDEF",
			},
		},
		{
			name: "TUIC",
			uri:  "tuic://33333333-3333-3333-3333-333333333333:password@tuic.example.com:443?sni=tls-tuic.example.com&alpn=h3&allow_insecure=1&congestion_control=bbr&udp_relay_mode=native&fast_open=1&disable_sni=1&reduce_rtt=1&udp_over_stream=1#tuic",
			want: map[string]any{
				"name": "tuic", "type": "tuic", "server": "tuic.example.com", "port": 443,
				"uuid": "33333333-3333-3333-3333-333333333333", "password": "password",
				"sni": "tls-tuic.example.com", "alpn": []string{"h3"}, "skip-cert-verify": true,
				"congestion-controller": "bbr", "udp-relay-mode": "native", "fast-open": true,
				"disable-sni": true, "reduce-rtt": true, "udp-over-stream": true,
			},
		},
		{
			name: "AnyTLS",
			uri:  "anytls://password@anytls.example.com:443?sni=tls-anytls.example.com&alpn=h2&insecure=1&fp=chrome&idleSessionCheckInterval=30&idleSessionTimeout=300&minIdleSession=2#anytls",
			want: map[string]any{
				"name": "anytls", "type": "anytls", "server": "anytls.example.com", "port": 443,
				"password": "password", "sni": "tls-anytls.example.com", "alpn": []string{"h2"},
				"skip-cert-verify": true, "client-fingerprint": "chrome",
				"idle-session-check-interval": 30, "idle-session-timeout": 300, "min-idle-session": 2,
			},
		},
		{
			name: "SOCKS5",
			uri:  "socks5://user:pass@socks.example.com:1080#socks5",
			want: map[string]any{
				"name": "socks5", "type": "socks5", "server": "socks.example.com", "port": 1080,
				"username": "user", "password": "pass", "udp": true,
			},
		},
		{
			name: "HTTPS 代理",
			uri:  "https://user:pass@http.example.com:8443#https-proxy",
			want: map[string]any{
				"name": "https-proxy", "type": "http", "server": "http.example.com", "port": 8443,
				"username": "user", "password": "pass", "tls": true,
			},
		},
	}

	producer := substore.NewURIProducer()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first, err := proxyparser.Parse(tc.uri)
			if err != nil {
				t.Fatalf("首次解析失败: %v", err)
			}
			assertFields(t, "首次解析", first, tc.want)

			produced, err := producer.ProduceOne(substore.Proxy(first))
			if err != nil {
				t.Fatalf("生成 URI 失败: %v", err)
			}
			if produced == "" {
				t.Fatal("生成 URI 为空")
			}

			second, err := proxyparser.Parse(produced)
			if err != nil {
				t.Fatalf("再次解析失败: %v\n生成 URI: %s", err, produced)
			}
			assertFields(t, "再次解析", second, tc.want)
			assertSameFields(t, first, second, tc.want)
		})
	}
}

func Test数值类型归一化(t *testing.T) {
	values := []any{int(443), int64(443), uint32(443), float64(443), json.Number("443.0")}
	for _, value := range values[1:] {
		if !normalizedEqual(values[0], value) {
			t.Fatalf("数值归一化失败: %T(%v)", value, value)
		}
	}
}

func assertFields(t *testing.T, stage string, got map[string]any, want map[string]any) {
	t.Helper()
	for path, expected := range want {
		actual, ok := lookupPath(got, path)
		if !ok {
			t.Errorf("%s缺少字段 %s\n完整结果: %#v", stage, path, got)
			continue
		}
		if !normalizedEqual(actual, expected) {
			t.Errorf("%s字段 %s 不一致: got=%#v (%T), want=%#v (%T)", stage, path, actual, actual, expected, expected)
		}
	}
}

func assertSameFields(t *testing.T, first, second map[string]any, fields map[string]any) {
	t.Helper()
	for path := range fields {
		left, leftOK := lookupPath(first, path)
		right, rightOK := lookupPath(second, path)
		if !leftOK || !rightOK || !normalizedEqual(left, right) {
			t.Errorf("往返字段 %s 不一致: first=%#v, second=%#v", path, left, right)
		}
	}
}

func lookupPath(root map[string]any, path string) (any, bool) {
	var current any = root
	for _, key := range strings.Split(path, ".") {
		switch value := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = value[key]
			if !ok {
				return nil, false
			}
		case map[string]string:
			var ok bool
			current, ok = value[key]
			if !ok {
				return nil, false
			}
		default:
			return nil, false
		}
	}
	return current, true
}

func normalizedEqual(left, right any) bool {
	return reflect.DeepEqual(normalize(left), normalize(right))
}

func normalize(value any) any {
	switch v := value.(type) {
	case json.Number:
		return normalizeNumber(v.String())
	case int:
		return normalizeNumber(strconv.FormatInt(int64(v), 10))
	case int8:
		return normalizeNumber(strconv.FormatInt(int64(v), 10))
	case int16:
		return normalizeNumber(strconv.FormatInt(int64(v), 10))
	case int32:
		return normalizeNumber(strconv.FormatInt(int64(v), 10))
	case int64:
		return normalizeNumber(strconv.FormatInt(v, 10))
	case uint:
		return normalizeNumber(strconv.FormatUint(uint64(v), 10))
	case uint8:
		return normalizeNumber(strconv.FormatUint(uint64(v), 10))
	case uint16:
		return normalizeNumber(strconv.FormatUint(uint64(v), 10))
	case uint32:
		return normalizeNumber(strconv.FormatUint(uint64(v), 10))
	case uint64:
		return normalizeNumber(strconv.FormatUint(v, 10))
	case float32:
		return normalizeNumber(strconv.FormatFloat(float64(v), 'g', -1, 32))
	case float64:
		return normalizeNumber(strconv.FormatFloat(v, 'g', -1, 64))
	case []string:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = normalize(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = normalize(item)
		}
		return out
	default:
		return value
	}
}

func normalizeNumber(value string) string {
	rational := new(big.Rat)
	if _, ok := rational.SetString(value); ok {
		return "number:" + rational.RatString()
	}
	return fmt.Sprintf("number:%s", value)
}
