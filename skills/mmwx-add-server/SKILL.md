---
name: mmwx-add-server
description: 在妙妙屋X里接入并初始化一台新的远程服务器——登记服务器、安装 Xray/Nginx、把入站同步成节点。当用户说"加一台服务器""新机器上线""给新 VPS 装好 xray 并出节点"时使用。
---

# 接入新服务器(妙妙屋X)

目标:让一台新服务器在妙妙屋X里可管理、装好代理内核、产出可用节点。

## 能力与授权检查

执行前读取当前连接的完整 `tools/list`（包含分页），核对本流程所需工具及 `inputSchema`。下文参数是兼容提示，以当前 schema 的必填项、类型和枚举为准。工具缺失或返回 403 时说明能力缺口，不猜工具名、不自动升级主控；可独立完成的只读步骤继续进行。复用当前任务已有授权，`confirm: true` 按 schema 传递，不重复索要已有授权。

## 步骤

1. **整理信息**：从当前请求、已验证配置和工具 schema 提取服务器名称、IP/域名、连接模式及端口；复用已给信息，只询问无法推断且会影响目标或部署的缺项。
2. **登记服务器**:先用 `server_list` 比对已有记录的 `ip_address / domain`;
   地址匹配且目标唯一时复用已有记录；同地址多实例且无法确定目标时才澄清。无匹配记录后调用 `server_create`,参数
   `name / ip_address / domain / connection_mode(默认 push)/ listen_port / traffic_limit`。
   再用 `server_list` 确认其已出现、拿到 `server_id`、查看连接状态。
3. **连接 agent**：如需安装，先核对目标机及安装脚本；已有 SSH/远程执行能力且用户授权接入安装时直接完成。缺访问能力时给出具体安装步骤，不空等。安装已启动后按工具返回的期限等待；没有期限时每 10 秒核对一次，最多 5 分钟。到期记录未连接和最后状态，停止轮询并转向日志诊断；长任务至少每分钟汇报一次。
4. **(可选)看资源**:`server_system_info`(传 server_id)看 CPU/内存/磁盘空间,确认机器健康再继续装服务。
5. **安装 Xray**:`server_xray_install`,参数 `server_id`、`confirm: true`(耗时操作,会等待完成并返回安装日志)。如需 Nginx 同理用 `server_nginx_install`。
6. **核对服务**:`server_service_status`(传 server_id)确认 xray 运行中。
7. **配置入站 → 出节点**:
   - 先准备入站配置，用 `server_xray_test_config`(传 server_id 和 config 对象)验证，再用 `server_inbound_apply`(action=add,传 inbound 对象)创建入站。已授权的配置由可用工具完成；仅在所需能力缺失时提供 Web 向导步骤。
   - 需要 reality 时:`server_reality_domains`(传 server_id)看候选目标域名,`xray_generate_x25519` 生成密钥对。
   - `server_inbound_list`(传 server_id)核对入站,`server_inbound_outbounds` 核对出站,`server_routing_get` 看路由。
   - `node_create` 把入站手动出节点(或 `server_sync_nodes` 批量同步),`node_list` 可见新节点。
8. **(按需)证书**：TLS 入站需要证书时，先用 `cert_list_valid` 查目标域名的有效证书；缺失且接入授权覆盖签发时，用 `cert_create`，等待本次签发成功后用 `cert_deploy` 部署并验证目标服务。域名、验证模式及 DNS 提供方按 schema 和已验证配置填写。仅在用户明确要求自签证书且客户端信任方案已明确时使用 `cert_self_signed`。
9. **(可选)升级 agent**:若日后该 agent 版本落后主控,`server_agent_upgrade`(server_id + confirm)远程升级(SSE,会短暂失联)。

## 注意
- 用户要求接入并装好服务已授权所需安装；只安装请求所需组件，不默认添加 Nginx 或升级。按当前 schema 传 `confirm: true`，该字段不要求再次聊天确认。耗时调用按宿主支持的异步方式等待，保留进度与实际结果。
- 卸载 xray/nginx、重置令牌等高危操作**未开放**给 agent,需人工在 Web 端处理。
- 装完若 `server_service_status` 显示未运行,检查安装日志返回里的报错;`server_xray_config_get` 拉取实际配置进一步定位。
- 同 IP 多服务器场景以 `server_list` 中的 `ip_address / domain` 比对结果为准。

- 安装或启动失败时，若当前主控支持 `logs_agent`，按目标服务器和服务读取必要日志；先脱敏，再引用具体错误。不同服务器使用各自配对信息，不复制令牌。
