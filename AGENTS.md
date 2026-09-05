# AGENTS.md

给在本仓库工作的编码 agent 的说明。

## 仓库是什么

妙妙屋X（miaomiaowux）的插件仓库，三个互不依赖的子项目：

| 目录 | 是什么 | 语言 |
|---|---|---|
| `proxyparser/` | 代理节点 URI 解析 + 各客户端格式转换的共享 Go module，对外发布供 miaomiaowu / miaomiaowux 引用 | Go，~24k 行 |
| `speedtest/` | 家用测速端 `mmwx-speedtester`，部署在用户家里，反向连接主控执行节点测速 | Go |
| `skills/` | 配合主控 MCP server 使用的 Claude Agent Skills | Markdown |

**`proxyparser/` 和 `speedtest/` 是两个独立的 Go module，各有自己的 `go.mod`。**
不存在仓库根部的 module，任何 go 命令都要先 `cd` 进对应目录。

## 项目归属

本仓库是 `mmwx-group/mmwX-plugins` 的 fork，但 **`zzulpc/mmwX-plugins` 是规范仓库**：

- fork 之后的改动**不回流上游**，两条线永久分叉
- Release、GHCR 镜像、安装脚本的下载源都以 `zzulpc` 为准
- 两个 `go.mod` 都使用 `github.com/zzulpc/mmwX-plugins/...` 规范路径

遇到 `MMWOrg` / `mmwx-group` 的残留引用，按上面的归属处理，不要自作主张指回上游。

## 构建与测试

```bash
# proxyparser
cd proxyparser && go build ./... && go vet ./... && go test ./... -count=1

# speedtest
cd speedtest && go build ./... && go vet ./... && go test ./... -count=1
```

基线（2026-08-26，Go 1.27.0）：两者都干净通过，`go vet` 无告警。
覆盖率（`go test ./... -cover -count=1`）：`proxyparser` 46.0%、
`proxyparser/internal/valueutil` 74.2%、`proxyparser/substore` 42.6%、`speedtest` 62.3%。
**改完测试顺手把这几个数对一遍**，基线错了会让人误以为新加的测试没生效。

**改完代码必须自己跑上面的命令**，不要只说「应该没问题」。

`speedtest` 的测试不需要真的起 mihomo / sing-box，也不要在测试里下载内核或访问外网。

## 代码风格

- **注释和提交信息用中文**，跟现有代码保持一致。
- 现有注释的风格是解释**为什么**这么写（约束、踩过的坑、对抗场景），不是复述代码在做什么。
  新增注释照这个标准写，不要写 `// 设置端口` 这种。
- `proxyparser/substore/` 是从一个 JS 实现移植过来的，很多函数注释里带 `(JS line NNN)`
  的对照标记，改动时保留这些标记。
- 不要顺手格式化整个文件、不要重构任务范围外的代码、不要动 `go.mod` 的依赖版本。

## 两个容易踩的地方

**1. `proxyparser` 有两条数据入口，类型不一样**

- **URI 路径**：`Parse(uri)` → `map[string]any`，其中 `ws-opts.headers` 是 `map[string]string`
- **YAML 路径**：clash 订阅经 `yaml.v3` 反序列化 → 嵌套全是 `map[string]any`

substore 的 `GetString` / `GetMap` / `GetInt` 等取值函数要同时兜住这两种。
历史上出过 bug：`GetMap` 只断言 `map[string]interface{}`，导致 URI 路径进来的
CDN 回源 Host 头被静默丢掉；对应双入口回归测试在 `proxyparser/substore/utils_test.go`。
**加测试时两条路径都要覆盖**，只测手写的 `map[string]any` fixture 是发现不了这类问题的。

**2. `Parse` 和 `URIProducer` 是一对互逆操作，但分居两个包**

`proxyparser.Parse()`（URI → map）和 `substore.URIProducer`（map → URI）
分别在根包和子包，两边各自的单测都绿不代表往返是对的。改任一侧时想一下另一侧。

## 发布

- `speedtest` 的发布由 tag `speedtest-vX.Y.Z` 触发 `.github/workflows/speedtest.yml`。
- `speedtest/VERSION` 的内容会 `//go:embed` 进二进制并上报给主控，**必须与 tag 版本一致**。
- `speedtest/scripts/release.sh` 会 bump 版本、改 changelog、打 tag 并 push。
  **agent 不要执行这个脚本**，它会真的推送到远端。

## 任务边界

仓库不保留已经执行完毕的一次性审计计划。新任务以当前用户请求或 Issue 为准；
**一个任务一个 commit**，任务里写了「不要动」的文件就真的别动。
