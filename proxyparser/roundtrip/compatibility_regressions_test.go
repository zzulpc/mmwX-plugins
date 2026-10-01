package roundtrip_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	parser "github.com/zzulpc/mmwX-plugins/proxyparser"
	"github.com/zzulpc/mmwX-plugins/proxyparser/substore"
	"gopkg.in/yaml.v3"
)

func Test凭据加号往返不变(t *testing.T) {
	got, err := parser.Parse("hysteria2://a+b@h.example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	if got["password"] != "a+b" {
		t.Fatalf("password=%q want=a+b", got["password"])
	}
}

func TestHysteria1认证往返(t *testing.T) {
	uri, err := substore.NewURIProducer().ProduceOne(substore.Proxy{"name": "hy1", "type": "hysteria", "server": "h.example.com", "port": 443, "auth-str": "mytoken", "up": 100, "down": 200})
	if err != nil {
		t.Fatal(err)
	}
	got, err := parser.Parse(uri)
	if err != nil {
		t.Fatalf("uri=%s parse error=%v", uri, err)
	}
	if got["auth-str"] != "mytoken" {
		t.Fatalf("auth-str=%#v", got["auth-str"])
	}
}

func TestVMessH2Host双入口往返(t *testing.T) {
	for _, entry := range []string{"URI", "YAML"} {
		t.Run(entry, func(t *testing.T) {
			var node map[string]any
			if entry == "URI" {
				data, _ := json.Marshal(map[string]any{"v": "2", "ps": "h2", "add": "edge.example.com", "port": "443", "id": "11111111-1111-1111-1111-111111111111", "aid": "0", "scy": "auto", "net": "h2", "tls": "tls", "sni": "tls.example.com", "host": "origin.example.com", "path": "/h2"})
				var err error
				node, err = parser.Parse("vmess://" + base64.StdEncoding.EncodeToString(data))
				if err != nil {
					t.Fatal(err)
				}
			} else {
				err := yaml.Unmarshal([]byte("name: h2\ntype: vmess\nserver: edge.example.com\nport: 443\nuuid: 11111111-1111-1111-1111-111111111111\nalterId: 0\ncipher: auto\nnetwork: h2\ntls: true\nsni: tls.example.com\nh2-opts:\n  path: /h2\n  host: [origin.example.com]\n"), &node)
				if err != nil {
					t.Fatal(err)
				}
			}
			uri, err := substore.NewURIProducer().ProduceOne(substore.Proxy(node))
			if err != nil {
				t.Fatal(err)
			}
			got, err := parser.Parse(uri)
			if err != nil {
				t.Fatal(err)
			}
			host := got["h2-opts"].(map[string]any)["host"]
			if !reflect.DeepEqual(host, []string{"origin.example.com"}) {
				t.Fatalf("h2-opts.host=%#v want=[origin.example.com]", host)
			}
		})
	}
}

func Test隐式TLS保留指定SNI(t *testing.T) {
	const uuid = "11111111-1111-1111-1111-111111111111"
	for typ, uri := range map[string]string{
		"trojan":    "trojan://pw@1.2.3.4:443",
		"hysteria2": "hysteria2://pw@1.2.3.4:443",
		"tuic":      "tuic://" + uuid + ":pw@1.2.3.4:443",
		"anytls":    "anytls://pw@1.2.3.4:443",
		"naive":     "naive+https://u:pw@1.2.3.4:443",
	} {
		for _, entry := range []string{"URI", "YAML"} {
			t.Run(typ+"/"+entry, func(t *testing.T) {
				var node map[string]any
				var err error
				if entry == "URI" {
					node, err = parser.Parse(uri + "?sni=cdn.example.com&insecure=1#tls")
				} else {
					err = yaml.Unmarshal([]byte(fmt.Sprintf("name: tls\ntype: %s\nserver: 1.2.3.4\nport: 443\npassword: pw\nusername: u\nuuid: %s\nsni: cdn.example.com\nskip-cert-verify: true\n", typ, uuid)), &node)
				}
				if err != nil {
					t.Fatal(err)
				}
				raw, err := substore.NewSingboxProducer().Produce([]substore.Proxy{node}, "internal", nil)
				if err != nil {
					t.Fatal(err)
				}
				out := raw.([]map[string]any)
				if len(out) != 1 {
					t.Fatalf("节点未输出: %#v", out)
				}
				tls := out[0]["tls"].(map[string]any)
				if tls["server_name"] != "cdn.example.com" {
					t.Fatalf("SNI 丢失: %#v", tls)
				}
			})
		}
	}
}

func TestWireGuard保活映射到Peer(t *testing.T) {
	for _, entry := range []string{"URI", "YAML"} {
		t.Run(entry, func(t *testing.T) {
			var node map[string]any
			var err error
			if entry == "URI" {
				node, err = parser.Parse("wireguard://private@1.2.3.4:51820?publickey=public&address=10.0.0.2/32&keepalive=25#wg")
			} else {
				err = yaml.Unmarshal([]byte("name: wg\ntype: wireguard\nserver: 1.2.3.4\nport: 51820\nprivate-key: private\npublic-key: public\nip: 10.0.0.2\nkeepalive: 25\n"), &node)
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := substore.NewSingboxProducer().Produce([]substore.Proxy{node}, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			var cfg map[string]any
			if err = json.Unmarshal([]byte(raw.(string)), &cfg); err != nil {
				t.Fatal(err)
			}
			endpoints := cfg["endpoints"].([]any)
			if len(endpoints) != 1 {
				t.Fatalf("endpoints=%#v", endpoints)
			}
			ep := endpoints[0].(map[string]any)
			for _, k := range []string{"keepalive", "persistent_keepalive", "server", "server_port", "peer_public_key"} {
				if v, ok := ep[k]; ok {
					t.Errorf("endpoint 包含未支持字段 %s=%#v", k, v)
				}
			}
			peer := ep["peers"].([]any)[0].(map[string]any)
			if peer["persistent_keepalive_interval"] != float64(25) {
				t.Errorf("peer 保活=%#v，期望 25", peer["persistent_keepalive_interval"])
			}
		})
	}
}

func TestHysteria端口范围解析(t *testing.T) {
	got, err := parser.Parse("hysteria2://pw@h.example.com:20000-30000?sni=cdn.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got["port"] != 20000 || got["ports"] != "20000-30000" {
		t.Fatalf("port=%#v ports=%#v want=20000 / 20000-30000", got["port"], got["ports"])
	}
}

// 同时覆盖真实 URI 和 YAML，尤其认证/混淆值中的分隔符不能在往返时变成额外参数。
func TestHysteria1特殊字段双入口往返(t *testing.T) {
	const raw = `name: "节点+一"
type: hysteria
server: "2001:db8::1"
port: 443
auth-str: "a+b &%?#/:"
sni: "cdn.example.com"
obfs: "obfs+ &=?"
_obfs: xplus
up: 100
down: 200
alpn: [h3, h2]
tfo: true
ports: "443,20000-30000"
`
	var original map[string]any
	if err := yaml.Unmarshal([]byte(raw), &original); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []string{"YAML", "URI"} {
		node := original
		if entry == "URI" {
			uri, err := substore.NewURIProducer().ProduceOne(substore.Proxy(node))
			if err != nil {
				t.Fatal(err)
			}
			node, err = parser.Parse(uri)
			if err != nil {
				t.Fatal(err)
			}
		}
		uri, err := substore.NewURIProducer().ProduceOne(substore.Proxy(node))
		if err != nil {
			t.Fatal(err)
		}
		got, err := parser.Parse(uri)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"name", "server", "port", "auth-str", "sni", "obfs", "_obfs", "tfo", "ports"} {
			if !reflect.DeepEqual(got[key], original[key]) {
				t.Errorf("%s %s: %#v != %#v", entry, key, got[key], original[key])
			}
		}
		if !reflect.DeepEqual(got["alpn"], []string{"h3", "h2"}) {
			t.Errorf("ALPN 丢失: %#v", got["alpn"])
		}
	}
}
