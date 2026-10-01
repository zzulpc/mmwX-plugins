# AGENTS.md

## 项目接手与持续记录

- 接续项目时先读 [docs/项目状态.md](docs/项目状态.md) 的相关部分；局部任务直接读取目标文件及适用规则。涉及运行、部署或恢复时再读[运行与恢复](docs/运行与恢复.md)，涉及迁移、文件布局或外部依赖时再读[迁移文件清单](docs/迁移文件清单.md)。
- 状态页是带日期的摘要；按本次动作核对实际文件、版本、数据或机器状态，说明差异，不把历史计划当成当前验收。
- 仅在项目状态、关键决定、验证结果或后续事项变化时更新状态入口；文件布局或依赖变化时同步迁移清单。只读核对、局部文字调整无需制造状态记录。
- 保留版本化历史证据；交接摘要只记必要结论，不复制旧对话、患者级原始资料或凭据。业务闸门与当前任务授权继续适用；恢复资料整理不等于实际恢复通过。

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

基线（2026-10-01，darwin/arm64，Go 1.26.8，与发布工具链一致）：两者都干净通过，`go vet` 无告警。
覆盖率（`go test ./... -cover -count=1`）：`proxyparser` 67.9%、
`proxyparser/internal/valueutil` 74.2%、`proxyparser/substore` 54.6%、`speedtest` 70.8%
（parser 仍有 4 个既有跳过用例；本机跳过 Windows 安装测试和 2 个可选的已安装
sing-box 校验；启用这些用例时应单独记录覆盖率）。
请在同一 Go 工具链下比较覆盖率，不要直接与旧 Go 1.27.0 基线混用；跨包往返测试
位于 `proxyparser/roundtrip`，其调用默认不计入被调用包的覆盖率。
**改完测试顺手把这几个数对一遍**，基线错了会让人误以为新加的测试没生效。

**修改哪个 Go module，就运行该 module 上述构建和测试命令**；涉及两个 module 时分别运行。仅修改 Skills 或文档时检查指令、引用与示例，不因此运行两个 module 的全套测试。
修改技能工具引用或清单脚本时运行 `python3 -m unittest discover -s skills/scripts -p 'test_*.py' -v`，同时维护 `skills/tool-capabilities.json` 与 README 允许名单。

`speedtest` 的测试不需要真的起 mihomo / sing-box，也不要在测试里下载内核或访问外网。

## 代码风格

- **注释和提交信息用中文**，跟现有代码保持一致。
- 现有注释的风格是解释**为什么**这么写（约束、踩过的坑、对抗场景），不是复述代码在做什么。
  新增注释照这个标准写，不要写 `// 设置端口` 这种。
- `proxyparser/substore/` 是从一个 JS 实现移植过来的，很多函数注释里带 `(JS line NNN)`
  的对照标记，改动时保留这些标记。
- 不要顺手格式化整个文件、不要重构任务范围外的代码、不要动 `go.mod` 的依赖版本。

## 三个容易踩的地方

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

**3. `speedtest` 有三条必须一起维护的不变量**

这三条都出过 bug，而且都属于「单看改动没问题、放进整体就错」的类型，各自有专门的用例钉着：

| 不变量 | 在哪 | 钉它的用例 |
|---|---|---|
| 吞吐速率的分母不含响应准备时间 | `downloadWindow`，单/多线程共用；计时只从第一个 2xx 起算 | `TestDownloadTimed单线程响应准备不占用吞吐窗口`、`TestDownloadTimed多线程响应准备不占用吞吐窗口` |
| 各阶段超时之和装得进执行预算，排队不挤压执行时间 | `runExecutionBudget` / `beginRun`（`runner.go`）；独立排队预算见 `runQueueWaitBudget`（`main.go`） | `TestRun执行预算能装下所有阶段超时`、`TestDispatchRunJob排队超时释放任务槽`、`TestBeginRun排队不消耗执行预算` |
| 生成配置里的节点名与主控下发的名字解耦 | `mihomoNodeTag` / `mihomoGroupTag` | `TestBuildMihomoConfig固定内部节点名` |

具体踩过的坑：

- **多线程测速曾系统性低估速率。** 单线程 v0.2.3 就把 setup 从计时里摘出去了，多线程当时漏掉，
  一直到 v0.2.6 才修。改动下载相关代码时，先确认自己没有把「协程起飞」当成「开始计时」。
- **加一个新阶段（或调大某个阶段的超时）必须同步 `runExecutionBudget`。**
  预算不闭合的表现不是超时，而是最后一个阶段报出「剩余时间不足以完成 8s 吞吐测试」，
  看起来像节点故障。
- **节点名来自订阅，是用户可控的。** 直接写进 mihomo 配置会撞上 DIRECT / REJECT / GLOBAL
  这些预置名，mihomo 以「名字重复」拒绝加载，用户只看得到「内核在端口就绪前退出」。

## 发布

- `speedtest` 的发布由 tag `speedtest-vX.Y.Z` 触发 `.github/workflows/speedtest.yml`。
- `speedtest/VERSION` 的内容会 `//go:embed` 进二进制并上报给主控，**必须与 tag 版本一致**。
- `speedtest/scripts/release.sh` 会 bump 版本、改 changelog、打 tag 并 push。
  仅在用户已明确授权对应版本的发布与推送后使用；执行前核对脚本、版本、变更、测试和目标远端。已有发布授权无须再次确认；普通修复或审阅请求不隐含发布授权。

## 任务边界

仓库不保留已经执行完毕的一次性审计计划。新任务以当前用户请求或 Issue 为准；
只有用户要求或已授权提交时才创建 commit，按一个完整变更组织提交；审阅和未要求提交的修复不自动提交。只暂存本次改动，保留已有修改，遵守用户指定的不可修改路径。
