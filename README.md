# 妙妙屋X 插件仓库

## 项目交接入口

- [当前状态与下一步](docs/项目状态.md)
- [运行与新设备恢复](docs/运行与恢复.md)
- [需要携带的文件及校验基线](docs/迁移文件清单.md)

最新源码维护日期：2026-10-01。已完成本地兼容性同步与测速修复；详情及验证边界见状态页。历史交接章节保留各自日期。

本仓库收录妙妙屋X（miaomiaowux）使用的共享模块、家用测速端和 Agent Skills。三个子项目互不依赖；仓库根目录不是 Go module，执行 Go 命令前请先进入对应子目录。

## 子项目

| 目录 | 用途 | 入口 |
|---|---|---|
| `proxyparser/` | 解析代理节点 URI，并将节点转换为多种客户端格式的 Go module | [proxyparser 使用说明](proxyparser/README.md) |
| `speedtest/` | 部署在用户家中的 `mmwx-speedtester`，反向连接主控并执行节点测速 | [speedtest 使用说明](speedtest/README.md) |
| `skills/` | 配合妙妙屋X 主控 MCP server 使用的 Claude Agent Skills | [Skills 使用说明](skills/README.md) |

## 构建与验证

分别进入两个 Go module 执行验证：

```bash
cd proxyparser
go build ./...
go vet ./...
go test ./... -count=1
```

```bash
cd speedtest
go build ./...
go vet ./...
go test ./... -count=1
```

技能工具清单与契约检查：

```bash
python3 -m unittest discover -s skills/scripts -p 'test_*.py' -v
```

## 许可证

本仓库采用 [MIT License](LICENSE)。`speedtest` 使用或分发的第三方组件另见其[第三方声明](speedtest/THIRD_PARTY_NOTICES.md)。
