# 妙妙屋X Claude Agent Skills

一组 Claude Agent Skills,配合妙妙屋X主控内置的 MCP server 使用,让 agent(如 OpenClaw)用自然语言完成常见运维。

## 前置:接好 MCP

1. 在妙妙屋X **个人设置 → API 令牌** 生成一枚令牌(权限与你的账号一致)。
2. 在你的 MCP 客户端里把妙妙屋X 配成一个远程(streamable-HTTP)MCP server,鉴权用 `Authorization: Bearer <令牌>`。下面给出两种常见客户端的写法。
3. 把本目录的各技能(`mmwx-*/`)放入客户端的 skills 目录(或 agent 工作区)。

### OpenClaw(`openclaw.json`)
```json
{
  "mcp": {
    "servers": {
      "miaomiaowux": {
        "url": "https://你的主控/mcp",
        "transport": "streamable-http",
        "headers": { "Authorization": "Bearer <你的 API 令牌>" }
      }
    }
  }
}
```

### Hermes Agent(`~/.hermes/config.yaml`,顶层加 `mcp_servers`)
```yaml
mcp_servers:
  miaomiaowux:
    url: "https://你的主控/mcp"
    headers:
      Authorization: "Bearer <你的 API 令牌>"
    connect_timeout: 15
    timeout: 600          # 关键:安装 xray/nginx 等工具会阻塞数分钟,超时给大点
    # 可选:只放开这 6 个技能会用到的工具（含按需的新版本能力）
    tools:
      include:
        - cert_create
        - cert_deploy
        - cert_list
        - cert_list_valid
        - cert_renew
        - cert_self_signed
        - custom_rule_list
        - forward_chain_apply
        - forward_chain_bind_node
        - forward_chain_connections
        - forward_chain_create
        - forward_chain_create_node
        - forward_chain_get
        - forward_chain_list
        - forward_chain_set_hops
        - forward_chain_set_limit
        - forward_group_create
        - forward_group_get
        - forward_group_list
        - forward_metrics
        - forward_status
        - logs_agent
        - logs_agent_files
        - logs_system
        - node_create
        - node_get
        - node_list
        - node_speedtest
        - node_speedtest_results
        - node_tcping
        - package_assign
        - package_assignment_list
        - package_assignment_upsert
        - package_create
        - package_list
        - server_agent_upgrade
        - server_create
        - server_inbound_apply
        - server_inbound_list
        - server_inbound_outbounds
        - server_list
        - server_nginx_install
        - server_reality_domains
        - server_routing_get
        - server_routing_update
        - server_service_control
        - server_service_status
        - server_sync_nodes
        - server_system_info
        - server_xray_config_get
        - server_xray_install
        - server_xray_test_config
        - speedtest_testers
        - subscribe_file_create
        - subscribe_file_list
        - subscribe_file_update
        - task_runs
        - temp_subscription_create
        - template_v3_analyze
        - template_v3_list
        - template_v3_preview
        - traffic_server_detail
        - traffic_snapshots
        - traffic_summary
        - traffic_user_detail
        - tunnel_list
        - user_create
        - user_detail
        - user_list
        - user_set_email
        - user_set_limits
        - user_set_remark
        - user_set_status
        - xray_generate_x25519
```
加完**重启 hermes**(MCP 在启动时连接);成功后日志会出现
`MCP server 'miaomiaowux' (HTTP): registered N tool(s)`。`N` 应与
完整 `tools/list` 中实际向客户端开放的数量一致；启用 `include` 时按允许名单交集计算，不同版本与权限也可能不同。

> 其它兼容 MCP 的客户端(Claude Code、Cursor 等)同理:填 `/mcp` 的 URL + Bearer 头即可。

## 工具速览(由 MCP server 暴露)

下方 67 项是已有的 `v0.4.9` 历史实测快照，不代表当前连接能力。本次新增的转发、证书、日志/任务、套餐实例及路由更新流程均有能力检查：执行前读取完整分页清单和当前 `inputSchema`，缺工具或权限时明确说明缺口，不能猜测或自动升级主控。

[`tool-capabilities.json`](tool-capabilities.json) 单独列出技能使用的 27 个可选新工具；这份声明只用于检查文档引用与客户端允许名单，不是运行时已验证的注册表，也不包含假设的 schema。本次没有连接主控验证这些能力。

<!-- MMWX-MCP-TOOLS:START -->
以官方主控 `v0.4.9` 的运行时 `tools/list` 实测结果为准，共 67 个工具；
对应客户端日志计数应为 `registered 67 tool(s)`：

- 自定义规则：`custom_rule_apply` `custom_rule_create` `custom_rule_delete`* `custom_rule_get` `custom_rule_list` `custom_rule_update`
- 节点：`node_batch_delete`* `node_create` `node_delete`* `node_get` `node_list` `node_speedtest` `node_speedtest_results` `node_tcping` `node_update`
- 套餐：`package_assign` `package_create` `package_delete`* `package_list` `package_unassign` `package_update`
- 旧版模板：`rule_template_get` `rule_template_list`
- 远程服务器：`server_agent_upgrade`* `server_create` `server_delete`* `server_inbound_apply` `server_inbound_create` `server_inbound_list` `server_inbound_outbounds` `server_list` `server_nginx_install`* `server_reality_domains` `server_routing_get` `server_service_control` `server_service_status` `server_sync_nodes` `server_system_info` `server_update` `server_xray_config_get` `server_xray_install`* `server_xray_test_config`
- 测速端：`speedtest_testers`
- 订阅：`subscribe_file_create` `subscribe_file_delete`* `subscribe_file_get` `subscribe_file_list` `subscribe_file_update` `temp_subscription_create`
- V3 模板：`template_v3_analyze` `template_v3_list` `template_v3_preview`
- 流量与隧道：`traffic_server_detail` `traffic_snapshots` `traffic_summary` `traffic_user_detail` `tunnel_list`
- 用户：`user_create` `user_delete`* `user_detail` `user_list` `user_set_email` `user_set_limits` `user_set_remark` `user_set_status`
- Xray 辅助：`xray_examples` `xray_generate_x25519`

<!-- MMWX-MCP-TOOLS:END -->

\* = 工具注册表要求在参数中加 `confirm: true` 才执行。未带星号不代表操作低风险;
例如重启服务、应用入站配置等操作须在当前任务授权范围内完成，已有授权不重复询问。
历史版本未开放令牌重置 / 清空节点 / 卸载 / 改管理员凭据等接口；当前能力以实际 schema 为准，不能仅凭文档推断。

> 注意:工具权限随 API 令牌所属账号。普通用户令牌调管理员工具会返回 403。

### 更新或核验工具清单

清单由 [`scripts/sync_mcp_tools.py`](scripts/sync_mcp_tools.py) 从 MCP 注册表生成，避免手写漂移。
历史快照取自官方 `v0.4.9` 的 `mmwx-darwin-arm64` 发布物（SHA-256：
`3547bf5d01ebe35ece06e99e5b76a0f544368cb53b4fc277a73b09712c77cace`），启动日志确认主控版本为
`0.4.9`；MCP 握手里的 server version 仍为 `0.1.0`，不用于判定主控发布版本。

对运行中的主控核验（从凭据管理器注入 `MMWX_API_TOKEN` 环境变量，令牌不写进命令参数或历史）：

```bash
python3 scripts/sync_mcp_tools.py \
  --url 'https://你的主控/mcp' --version v0.4.9 --check
```

也可先保存标准 `tools/list` JSON 响应，再离线核验；确认差异后把 `--check` 换成 `--write`
即可更新标记区块：

```bash
python3 scripts/sync_mcp_tools.py \
  --input /tmp/mmwx-tools-list.json --version v0.4.9 --check
```

`--url` 会收集全部分页；离线响应若仍有 `nextCursor` 会拒绝写入，重复名称或循环游标也会报错。`--version` 填实际核对的主控发布版本，不能照抄历史值。

需要检查参数变化时可同时生成输入契约快照（路径须与 README 不同，父目录预先存在）：

```bash
python3 scripts/sync_mcp_tools.py \
  --input /tmp/mmwx-tools-list.json --version '<实际版本>' \
  --schema /tmp/mmwx-input-contract.json --write
```

后续使用同一路径并改为 `--check`，会同时比较名称及 `required`、类型、枚举、`confirm` 等约束。快照剔除说明、标题、默认值和示例，不保存运行结果；因此默认值变化不在自动检查范围，仍需对照实际 schema。原始响应和枚举/常量可能带环境专属值，公开前需人工检查。脚本只调用只读 `tools/list`，不会执行运维工具。

本地/CI 校验（无需令牌或外网）：

```bash
python3 -m unittest discover -s skills/scripts -p 'test_*.py' -v
```
