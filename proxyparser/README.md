# proxyparser

`proxyparser` 是妙妙屋X 的代理节点解析与格式转换 Go module。它可以把单条代理 URI 或订阅内容解析为通用节点映射，再通过 `substore` producer 输出为不同客户端格式。

本 module 由 `zzulpc/mmwX-plugins` 独立维护，与上游 `mmwx-group/mmwX-plugins` 使用的 `github.com/MMWOrg/mmwX-plugins/proxyparser` 是两条独立发布线，请勿混用。

## 安装

```bash
go get github.com/zzulpc/mmwX-plugins/proxyparser
```

## 支持解析的协议

`Parse` 当前支持以下协议及 URI scheme：

| 协议 | URI scheme |
|---|---|
| VMess | `vmess://` |
| Shadowsocks | `ss://` |
| ShadowsocksR | `ssr://` |
| SOCKS | `socks://`、`socks5://` |
| Trojan | `trojan://` |
| VLESS | `vless://` |
| Hysteria | `hysteria://` |
| Hysteria 2 | `hy2://`、`hysteria2://` |
| TUIC | `tuic://` |
| AnyTLS | `anytls://` |
| WireGuard | `wireguard://`、`wg://` |
| HTTP 代理 | `http://`、`https://` |
| NaiveProxy | `naive://`、`naive+http://`、`naive+https://` |
| Mieru | `mieru://`、`mierus://` |
| Snell | `snell://` |

## 解析单条 URI

`Parse` 返回适合继续交给 `substore` 处理的节点映射：

```go
package main

import (
	"fmt"
	"log"

	"github.com/zzulpc/mmwX-plugins/proxyparser"
)

func main() {
	node, err := proxyparser.Parse("ss://YWVzLTEyOC1nY206cGFzc3dvcmQ@example.com:8388#示例节点")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("%#v\n", node)
}
```

## 解析订阅

`ParseSubscription` 接受 Base64 编码的订阅，也接受按行排列的明文节点 URI。当前还会尝试解析非 URI 的 Surge Snell 节点行；无法识别或解析失败的行会被跳过。

```go
package main

import (
	"fmt"
	"log"

	"github.com/zzulpc/mmwX-plugins/proxyparser"
)

func main() {
	content := "trojan://secret@example.com:443#节点一\n" +
		"vless://00000000-0000-0000-0000-000000000000@example.net:443#节点二"

	nodes, err := proxyparser.ParseSubscription(content)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("共解析 %d 个节点\n", len(nodes))
}
```

## 使用 substore producer

将解析结果转换成 `substore.Proxy` 后，可以通过默认工厂选择输出格式：

```go
package main

import (
	"fmt"
	"log"

	"github.com/zzulpc/mmwX-plugins/proxyparser"
	"github.com/zzulpc/mmwX-plugins/proxyparser/substore"
)

func main() {
	node, err := proxyparser.Parse("trojan://secret@example.com:443#示例节点")
	if err != nil {
		log.Fatal(err)
	}

	proxies := []substore.Proxy{substore.Proxy(node)}
	result, err := substore.GetDefaultFactory().ConvertProxies(proxies, "clashmeta", nil)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Print(result)
}
```

需要直接控制 producer 时，也可以使用 `GetProducer`：

```go
producer, err := substore.GetDefaultFactory().GetProducer("uri")
if err != nil {
	log.Fatal(err)
}

result, err := producer.Produce(proxies, "", nil)
if err != nil {
	log.Fatal(err)
}
```

## 支持的输出格式

默认工厂在 `substore/factory.go` 中注册以下输出类型；调用 `ConvertProxies` 或 `GetProducer` 时使用表中的类型值：

| 类型值 | 输出格式 |
|---|---|
| `clash` | Clash |
| `clashmeta` | Clash Meta / Mihomo |
| `surfboard` | Surfboard |
| `uri` | 节点 URI 列表 |
| `v2ray` | Base64 编码的 V2Ray 订阅 |
| `shadowrocket` | Shadowrocket |
| `surge` | Surge |
| `surgemac` | Surge for Mac |
| `stash` | Stash |
| `qx` | Quantumult X |
| `loon` | Loon |
| `sing-box` | sing-box |
| `egern` | Egern |

不同客户端支持的协议和字段并不完全相同。producer 会按自身兼容规则转换、过滤或报告不支持的节点；调用方不应假设每个输入节点都会出现在每种输出中。

## 验证

```bash
go build ./...
go vet ./...
go test ./... -count=1
```

## 2026-10-01 兼容性更新

已参考上游截至 `ab5e17ea22a2b430621a6b0c1da5cd8ed6a25871` 的变更，保留本仓库的规范模块路径与既有修复。来源和验证记录见[项目状态](../docs/项目状态.md)。

- URI 凭据保留字面加号，查询参数采用表单解码；补齐 Hysteria 1 的 `auth-str`/混淆字段、VMess H2 Host、AnyTLS REALITY 和 WireGuard 字段往返。
- WireGuard 在 sing-box 的 endpoint/peer 层级输出；补齐隐式 TLS 的 SNI、各客户端字段及支持协议。客户端自身支持范围仍需以实际版本验证。
- V3 模板支持嵌套 YAML 合并与别名，保留显式内置出站。缺失的代理集合、无法等价转换的筛选表达式会报错，不扩大节点选择范围。
- Surge、Shadowrocket 与 Loon 完整配置会清理已知未输出的节点引用；规则或代理链因此失效时返回错误，不自动改成直连。调用方需显式选择替代策略。
- `BuildLoonKeleeConfigWithPolicies` 保留接口，但仅提供一个外部组名不足以还原它的成员；引用模板中不存在的组会报错。需要完整组定义时使用 `BuildCompleteLoonConfig`，或在模板确实含该组时使用 `BuildLoonProxySections`。
- `SetLoonRuleSetResolver` 支持调用方提供规则集解析结果；解析器本身不联网下载规则集。无法转换的规则在输出中明确标注，不能据生成成功推断与来源规则完全等价。
