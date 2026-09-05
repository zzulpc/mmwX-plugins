# 第三方组件说明

本容器包含以下独立运行的第三方程序：

## Mihomo

- 项目：MetaCubeX/mihomo
- 固定版本：`v1.19.30`
- 固定源码提交：`ac017cdd246ce8bd547653d927e7bf77d7ee73d5`
- Linux amd64 资产：`mihomo-linux-amd64-compatible-v1.19.30.gz`
  - SHA-256：`db214c7a2517e63c150d123178d16d102e03a241ccdae4e5e07ffbe9cf56c6f9`
- Linux arm64 资产：`mihomo-linux-arm64-v1.19.30.gz`
  - SHA-256：`58896873736d28628f66de3677c8654fa0f180662523148e136cff4f6e890069`
- 上游源码：<https://github.com/MetaCubeX/mihomo/tree/v1.19.30>
- 许可证：GNU General Public License v3.0
- GPLv3 完整正文：[`LICENSES/GPL-3.0.txt`](LICENSES/GPL-3.0.txt)
- 对应源码与校验方式：[`CORRESPONDING_SOURCE.md`](CORRESPONDING_SOURCE.md)

测速器通过子进程调用 Mihomo，没有把 Mihomo 源码链接进测速器二进制。本镜像使用上游官方、
未经修改的 Mihomo 二进制，但不是 MetaCubeX 官方产品，也不代表与 MetaCubeX 存在关联。

## sing-box

- 项目：SagerNet/sing-box
- 固定版本：`v1.14.0`
- 固定源码提交：`0b8995879f29a9b98ee027bc17b75e101445b238`
- 容器清单摘要：`sha256:4bed9332a0013fef72c31200a84e8fc0ed91a5ab2fe373a69f0acbbbbfbef3c5`
- 上游源码：<https://github.com/SagerNet/sing-box/tree/v1.14.0>
- 许可证：GNU General Public License v3.0 or later
- 上游许可声明：[`LICENSES/sing-box-LICENSE.txt`](LICENSES/sing-box-LICENSE.txt)
- GPLv3 完整正文：[`LICENSES/GPL-3.0.txt`](LICENSES/GPL-3.0.txt)
- 对应源码与校验方式：[`CORRESPONDING_SOURCE.md`](CORRESPONDING_SOURCE.md)

测速器通过子进程调用 sing-box，没有把 sing-box 或 sing-snell 源码链接进测速器二进制。
本镜像使用上游官方、未经修改的 sing-box 二进制，但不是 SagerNet 官方产品，也不代表与
SagerNet 存在关联。

## Go 依赖

- `github.com/gorilla/websocket v1.5.3`：BSD 2-Clause，正文见
  [`LICENSES/gorilla-websocket-BSD-2-Clause.txt`](LICENSES/gorilla-websocket-BSD-2-Clause.txt)。
- `gopkg.in/yaml.v3 v3.0.1`：MIT 与 Apache-2.0，项目声明见
  [`LICENSES/go-yaml-v3-LICENSE.txt`](LICENSES/go-yaml-v3-LICENSE.txt)，Apache-2.0 完整正文见
  [`LICENSES/Apache-2.0.txt`](LICENSES/Apache-2.0.txt)。
- mmwx-speedtester 自身：MIT，正文见
  [`LICENSES/mmwx-speedtester-MIT.txt`](LICENSES/mmwx-speedtester-MIT.txt)。
