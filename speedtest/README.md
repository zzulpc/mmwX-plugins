# mmwx-speedtester · 妙妙屋X 家用测速端

妙妙屋X PRO「节点测速」功能的家用测速端(Phase 2)。部署在你家里的服务器/电脑上,
**主动反向连接主控**(解决家庭无公网 IP、主控无法主动访问的问题);收到主控派发的
测速任务后,用 [Mihomo](https://github.com/MetaCubeX/mihomo) 或 [sing-box](https://github.com/SagerNet/sing-box) 内核对指定节点下载测速,
结果经同一连接回传。从而得到「你家这条网络 → 节点」的真实速度。

## Snell v6 双内核 PoC

当前 Mihomo 尚不能完成 Snell v6 测速，因此测速端按节点配置自动分流：

- `type=snell` 且 `version=6`：使用 sing-box 1.14；
- Snell v4/v5 及其他全部协议：继续使用 Mihomo，原有路径不变。

这一版只承载现有 HTTP/HTTPS 测速所需的 TCP 流量，sing-box 出站固定为 `network: tcp`，
暂不把 UDP 测试算作已支持。Snell v6 的 `default`、`unshaped`、`unsafe-raw` 三种 mode
均按主控配置原样转换；`unsafe-raw` 缺少协议层加密，不建议使用。

Dockerfile 固定使用 Mihomo `v1.19.30` 与 sing-box `v1.14.0-beta.17`；构建时按目标架构
校验官方资产 SHA-256，容器运行期间不再下载代理内核。sing-box 仍是测试版，因此固定
官方镜像清单摘要，不使用 `latest-testing`。第三方来源与许可证见
[`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md)，对应源码见
[`CORRESPONDING_SOURCE.md`](CORRESPONDING_SOURCE.md)。

## 使用

1. 在妙妙屋X 主控「节点测速 → 管理测速端」生成一个**配对令牌**。
2. 从规范仓库 [`zzulpc/mmwX-plugins` 的最新 Release](https://github.com/zzulpc/mmwX-plugins/releases/latest)
   下载对应平台的 `mmwx-speedtester`。仓库中的 Linux/macOS 与 Windows 安装脚本也只从这个
   Release 下载二进制及 `checksums.txt`，不要使用 `MMWOrg` 或 `mmwx-group` 的旧下载入口。
3. 在你家里的机器上运行(支持 Linux / Windows / macOS,amd64/arm64):

```bash
# Linux / macOS
MMWX_MASTER=https://你的主控地址 \
MMWX_SPEEDTEST_TOKEN=<配对令牌> \
MMWX_SPEEDTEST_NAME=家里 \
MMWX_DATA_DIR="$HOME/.local/share/mmwx-speedtester" \
./mmwx-speedtester
```

```powershell
# Windows PowerShell
$env:MMWX_MASTER = "https://你的主控地址"
$env:MMWX_SPEEDTEST_TOKEN = "<配对令牌>"
$env:MMWX_SPEEDTEST_NAME = "家里"
$env:MMWX_DATA_DIR = "$env:LOCALAPPDATA\mmwx-speedtester"
.\mmwx-speedtester-windows-amd64.exe
```

推荐使用环境变量，因为命令行参数会出现在进程参数列表中，可能被同机用户读取，也会留在 shell 历史里。
兼容用的 `-master`、`-token`、`-name` 参数仍保留，但不建议用它们传递配对令牌。
`-data-dir` 与 `MMWX_DATA_DIR` 用于指定持久数据目录，内核缓存在其 `bin/` 子目录。
未配置时使用可执行文件旁的 `data/bin/`，不受启动目录变化影响；若旧的
`./data/bin/` 已有内核，则继续沿用，避免老用户重新下载。

4. 主控「节点测速」下拉即可看到该测速端,选它即可从家庭网络视角测速。

> Docker 镜像已同时包含 Mihomo 与 sing-box，不需要在容器启动或首次测速时访问 GitHub。
> 镜像通过绝对路径使用这两个内核，不依赖 `PATH` 搜索。直接运行独立二进制时，启动预热阶段
> 若未找到合格的 Mihomo，才会下载并校验对应平台的固定版本；也可用
> `MIHOMO_BIN=/path/to/mihomo` 与 `SING_BOX_BIN=/path/to/sing-box` 显式指定内核。
> 这两个变量都是失败关闭:指向的文件不存在、不可执行或版本过低时直接报错，
> 不会回退到数据目录、`PATH` 或网络下载。

## 任务容量与“测速端忙”

为了避免多个节点同时抢占家庭带宽导致结果失真，每个测速端实际一次只执行一个带宽测速，
同时最多接纳 4 个在途任务（1 个执行、其余等待）。第 5 个及后续任务会立即回传
`status: failed`、`error: 测速端忙`，不会无限积压。

主控批量选择较多节点时可能并发派发任务。出现“测速端忙”表示测速端当时容量已满，
不代表节点本身故障；应等待数秒后只重试这些任务，再根据重试结果判断节点状态。

可达性探测（`probe`）另有一套限额：单条消息最多 200 个目标，全进程同时最多 16 路 TCP 拨测
（多条探测共享这 16 路，不是每条各占 16 路），在途探测任务最多 8 个。超出在途上限的探测会被
直接丢弃且**不回包** —— 回一条 `results` 为空的 `probe_result` 会被读成“这批目标全部被墙”，
比不回包更容易造成误判。每个任务的整体截止时间按目标批次和单次拨号超时计算，最大约 200 秒；
主控连接断开或整体超时后会立即取消等待与拨号，也不会回传不完整结果。正常节奏（几十个节点
每 5 分钟一轮）不会触及这些上限。

## Docker 构建

在本目录构建 PoC 镜像：

```bash
docker build -t mmwx-speedtester:snell-v6-poc .
```

公开发行版发布到 `ghcr.io/zzulpc/mmwx-speedtester`。发布流程只接受
`speedtest-vX.Y.Z` 格式的正式标签，并只生成完整版本标签与提交标签，不自动更新
`latest`。`speedtest-v0.2.5` 对应：

```bash
docker pull ghcr.io/zzulpc/mmwx-speedtester:0.2.5
```

镜像不包含主控地址、测速端名称、配对令牌或节点密钥。`MMWX_MASTER`、
`MMWX_SPEEDTEST_NAME` 和 `MMWX_SPEEDTEST_TOKEN` 必须在容器启动时传入。镜像已默认设置
`MIHOMO_BIN=/usr/local/bin/mihomo` 与 `SING_BOX_BIN=/usr/local/bin/sing-box`，
所以已有 `/data/bin/` 下的内核会被忽略，旧数据卷不必删除；
`MMWX_DATA_DIR=/data` 继续保留兼容性，需要改用其它挂载点时可在运行时覆盖。不要把配对令牌
写入 Dockerfile、构建参数或公开 Compose 文件。

容器继续以非 root 用户运行，不需要 `privileged`、TUN 或 `CAP_NET_ADMIN`。每次测速的
代理配置只写入系统临时目录，目录权限为 `0700`、文件权限为 `0600`，成功或失败都会清理。
sing-box mixed 入站只监听随机的 `127.0.0.1` 端口，不暴露到 macvlan 局域网。

如果 Compose 使用只读根文件系统，需要同时为 `/tmp` 增加可写 tmpfs，例如：

```yaml
read_only: true
tmpfs:
  - /tmp:rw,noexec,nosuid,nodev,size=64m
security_opt:
  - no-new-privileges:true
cap_drop:
  - ALL
```

## 构建

```bash
go build -o mmwx-speedtester .
# 交叉编译 Windows
GOOS=windows GOARCH=amd64 go build -o mmwx-speedtester.exe .
```

本地二进制构建不自动捆绑代理内核：Mihomo 沿用原有自动下载逻辑，Snell v6 需要另外安装
sing-box 1.14+ 并放入 `PATH`，或设置 `SING_BOX_BIN`。

## 验证

```bash
go test ./...
go vet ./...
go build ./...
```

单元测试会验证双内核选择、内置 Mihomo 固定资产、Snell v6 字段转换、密钥脱敏、
单线程断流重试与失败路径临时目录清理。
如果测试环境存在 sing-box 1.14+，还会实际执行 `sing-box check` 校验生成的 JSON。

发布:`bash scripts/release.sh [patch|minor|major]`(打 `speedtest-vX.Y.Z` tag,GitHub Action 自动多平台打包)。

<details>
<summary>更新日志</summary>

### v0.2.5 (2026-08-26)
- 修复测速端安全、资源与发布边界

### v0.2.4 (2026-08-26)
- Snell v6 测速固定关闭跨请求连接复用，避免前序短探测的连接状态影响首次单线程下载。
- 首个有效响应前最多执行三次有界尝试，退避仍计入原有准备窗口，且不扩展测速时间或流量额度。

### v0.2.3 (2026-08-25)
- 将固定版本的 Mihomo 直接内置进 amd64/arm64 容器镜像，运行期不再下载内核。
- 修复 Snell v6 单线程大文件在响应头前遇到 EOF 后直接失败的问题。
- 收紧测速计时、父任务超时、多线程断流与共享额度结束的成功判定。
- 发布时校验并附带 Mihomo 对应源码、许可证与校验摘要。

### v0.2.2 (2026-08-25)
- 为测速端发布脚本增加闸门
- 修复安装脚本平台检测
- 关闭测速数据目录的容器兼容缺口
- 兼容主控批量测速的忙状态
- 删除未使用的内核状态函数
- 固定并校验 Mihomo 下载资产
- 校验测速端安装包摘要
- 统一测速端安装脚本的规范下载源
- 让测速内核缓存脱离启动目录
- 迁移 Go module 到规范仓库路径
- 避免在进程参数中暴露配对令牌
- 限制多线程测速总下载量
- 限制测速任务在途数量

### v0.2.1 (2026-08-25)
- 修复正式发布时误下载 Docker 构建记录导致 Release 附件打包失败的问题。

### v0.2.0 (2026-08-25)
- Snell v6 测速自动分流到固定版本的 sing-box，其他协议继续使用 Mihomo。
- 增加内核输出脱敏、临时配置权限收紧与失败路径清理。
- 公开镜像附带第三方许可证、对应源码说明、SBOM 与构建来源证明。

### v0.1.5 (2026-07-26)
- fix 没有ipv6检测误报

### v0.1.4 (2026-07-22)
- 🌈 支持服务器IP可用性探测

### v0.1.3 (2026-07-20)
- 🌈 支持设置缓冲区大小

### v0.1.2 (2026-07-08)
- fix 脚本错误

### v0.1.1 (2026-07-08)
- speedtest支持snell mihomo

### v0.1.0 (2026-06-10)
- 🌈增加自动重连与docker镜像打包

### v0.0.9 (2026-06-06)
- 🌈增加链接日志

### v0.0.8 (2026-05-26)
- 🌈优化单线程测速

### v0.0.7 (2026-05-26)
- Update install.sh
- 🌈 支持延迟测试

### v0.0.6 (2026-05-26)
- 🌈测速的时间改为8秒

### v0.0.5 (2026-05-26)
- 🌈测速的时间改为15秒

### v0.0.4 (2026-05-26)
- 🌈增加测速安装脚本
- 🌈支持多线程测速

### v0.0.3 (2026-05-23)
- 🌈测速结果支持出口IP显示

### v0.0.2 (2026-05-22)
- 🌈主控测速插件
</details>
