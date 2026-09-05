# 容器内代理内核对应源码

本项目的双内核容器包含上游官方、未经修改的 Mihomo 与 sing-box 二进制，均作为独立
子进程调用。它不是 MetaCubeX 或 SagerNet 官方产品，也不代表本项目与上游存在关联。

## Mihomo

对应关系如下：

- 版本：`v1.19.30`
- 完整提交：`ac017cdd246ce8bd547653d927e7bf77d7ee73d5`
- 对应源码：<https://github.com/MetaCubeX/mihomo/archive/refs/tags/v1.19.30.tar.gz>
- 本次固定源码包 SHA-256：`ee8a7107707e4bd485460139b1944e7be30016393783f2b4e928c14880c8ca8b`

每个包含内置 Mihomo 的正式 GitHub Release 都会同时附带文件
`mihomo-v1.19.30-source.tar.gz`。发布流程会先核对上述 SHA-256，再上传同一份源码包；
如果上游归档内容发生变化，发布会失败，而不会悄悄附带未经核验的源码。许可证正文见
[`LICENSES/GPL-3.0.txt`](LICENSES/GPL-3.0.txt)。

## sing-box

对应关系如下：

- 版本：`v1.14.0`
- 完整提交：`0b8995879f29a9b98ee027bc17b75e101445b238`
- 官方容器清单摘要：`sha256:4bed9332a0013fef72c31200a84e8fc0ed91a5ab2fe373a69f0acbbbbfbef3c5`
- 对应源码：<https://github.com/SagerNet/sing-box/archive/refs/tags/v1.14.0.tar.gz>
- 本次固定源码包 SHA-256：`87baf6852e37941cbe40bdd94bec81c957c88a56751cecd6bbf0e6108bc69398`

每个正式 GitHub Release 都会同时附带文件
`sing-box-v1.14.0-source.tar.gz`。发布流程会先核对上述 SHA-256，再上传同一份源码包；
如果上游归档内容发生变化，发布会失败，而不会悄悄附带未经核验的源码。

该源码包包含构建 sing-box 所需的源文件、模块依赖声明与上游构建配置。只要本项目继续提供
包含此 sing-box 二进制的公开镜像，就会在对应 Release 中保持源码包可免费访问。许可证与
附加名称条款见 [`LICENSES/sing-box-LICENSE.txt`](LICENSES/sing-box-LICENSE.txt)，GPLv3
完整正文见 [`LICENSES/GPL-3.0.txt`](LICENSES/GPL-3.0.txt)。
