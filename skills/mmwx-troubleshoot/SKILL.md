---
name: mmwx-troubleshoot
description: 排查妙妙屋X的常见故障——节点离线、服务器掉线、xray 未运行、用户订阅异常/无法连接。当用户说"节点连不上""服务器离线了""某用户用不了""xray 挂了"时使用。
---

# 故障排查(妙妙屋X)

目标：从只读证据定位问题，并在用户已授权的修复范围内完成修复和验证。仅要求诊断时给出具体方案；已要求修复或已批准方案时不逐个写操作重复确认。

## 能力与授权检查

执行前读取当前连接的完整 `tools/list`（包含分页），核对本流程所需工具及 `inputSchema`。下文参数是兼容提示，以当前 schema 的必填项、类型和枚举为准。工具缺失或返回 403 时说明能力缺口，不猜工具名、不自动升级主控；可独立完成的只读步骤继续进行。复用当前任务已有授权，`confirm: true` 按 schema 传递，不重复索要已有授权。

## A. 服务器/服务层面
1. `server_list` 看目标服务器 `status` 与 `xray_running`。
2. **资源排查**:`server_system_info`(传 server_id)看 CPU/内存/磁盘是否打满——OOM 或磁盘满会导致 xray 反复挂掉。
3. 若 connected 但 xray 未运行：用 `server_service_status` 确认。目标明确且用户已授权恢复该服务时，说明短暂中断影响后，用 `server_service_control`(server_id / service=xray / action=restart)重启并回读状态；只有继续会超出既有授权时才请求补充授权。
4. 若服务器 disconnected：先检查已有连接证据；有目标机访问工具且已获修复授权时继续诊断 agent 与网络，否则说明缺少的访问能力和具体下一步，不把 disconnected 直接当作已确认的网络故障。
5. **同 IP 排查**:在 `server_list` 结果中比对 `ip_address / domain`,确认是否有重复登记。

## B. 节点层面
1. `node_list` 找到目标节点,`node_get`(传 id)看完整配置;核对其 `server`/`port`/`inbound_tag`。
2. **TCP 连通性诊断**:`node_tcping`(host=节点 server,port=节点端口)从主控视角探测能否打通,排除中间网络问题。
3. `tunnel_list` 看该节点是否被 tunnel 转发、转发是否正常。
4. 需要时 `server_inbound_list`(传 server_id)核对入站是否存在、配置是否匹配。
5. 配置层深挖:`server_xray_config_get`(传 server_id)拉完整 xray 配置看路由/入站细节;`server_routing_get` 看路由规则;`custom_rule_list` 看自定义分流规则是否冲突。
6. 怀疑链路速度问题时,触发 `mmwx-node-speedtest` 技能测速佐证。

## B2. 日志、采集、转发与证书

- 日志能力可用时，`logs_system` 查主控错误，`logs_agent_files` 查可读文件，`logs_agent` 按服务器、服务、时间范围取必要日志；引用前脱敏，不输出节点凭据或令牌。`task_runs` 核对采集失败，不能把采集停滞直接归因于节点。
- 走转发链时，用 `forward_status` 核对下发与监听，`forward_chain_list` / `forward_chain_get` 看跳序，`forward_group_list` 看成员；`forward_chain_connections` 和 `forward_metrics` 区分连接受限、未到入口和链路异常。已授权修复的变更完成后用 `forward_chain_apply` 下发，关联本次任务回读状态。
- `cert_list` 核对目标证书、到期及部署状态。证书恢复在当前授权内时执行 `cert_renew`，签发成功后再 `cert_deploy`；回读服务使用的证书，不能只凭续期接口成功就判断连接恢复。

## C. 用户层面
1. `user_detail`(传 username)看其状态(是否被禁用)、套餐、配额。支持实例模型时追加 `package_assignment_list` 核对全部实例的生效状态、额度和到期时间。
2. `traffic_user_detail`(传 username)看是否已超额(超额会被限速/阻断)。
3. **订阅排查**：用 `subscribe_file_list` 检查订阅；确认缺失且修复授权覆盖该用户的订阅恢复时，使用 `subscribe_file_create`，并验证新订阅。仅诊断请求只报告缺失及修复方案。
4. 结论可能是:用户被禁用(可 `user_set_status` 启用)、超额(可 `user_set_limits` 或换套餐)、未绑定套餐(`package_assign`)、订阅文件缺失(`subscribe_file_create`)。先判断具体变更是否在已有授权内；涉及变更套餐、提高配额或恢复被禁用账号等业务决定时，不从普通连接故障推导授权。

## D. 模板/规则配置层面
1. **预览订阅**:若用户报订阅内容异常,用 `template_v3_analyze`(传 subscription_url)分析节点分布,或 `template_v3_preview`(传模板内容 + 节点)直接渲染对比。
2. `custom_rule_list` 看自定义分流是否影响了用户走向(命中 DIRECT 错路等)。

## 注意
- 先诊断，再按完整修复方案执行并验证；进度说明不要求用户回复。仅在目标不明确、影响超出已授权范围或存在新的不可逆后果时补充确认。
- 按当前工具 schema 传参；`confirm: true` 是调用字段，不自动构成新的聊天确认要求，也不代替用户授权。
- 卸载/重置类操作不开放,遇到需要这类处理的情况,给人工指引。
- `server_xray_config_get` 拉取的是远程服务器的实际生效配置(诊断金标准),与"主控记录的应有配置"比对可发现飘移。

- 路由调整在已有修复授权内且支持 `server_routing_update` 时，先回读当前路由并按稳定标识定位目标规则，操作后验证实际配置；不能凭旧下标删除规则，也不能把失败的代理链静默改成直连。
