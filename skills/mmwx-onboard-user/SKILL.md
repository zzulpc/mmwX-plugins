---
name: mmwx-onboard-user
description: 在妙妙屋X里开通一个新用户的完整流程——创建账号、选择或新建套餐、绑定、生成订阅链接。当用户说"开通/新增一个用户""给某人开个套餐""新建账号并配置好节点"时使用。
---

# 开通新用户(妙妙屋X)

目标:从零给一个新用户配好可用的订阅。

## 能力与授权检查

执行前读取当前连接的完整 `tools/list`（包含分页），核对本流程所需工具及 `inputSchema`。下文参数是兼容提示，以当前 schema 的必填项、类型和枚举为准。工具缺失或返回 403 时说明能力缺口，不猜工具名、不自动升级主控；可独立完成的只读步骤继续进行。复用当前任务已有授权，`confirm: true` 按 schema 传递，不重复索要已有授权。

## 步骤

1. **确认信息**:向用户索取 用户名、初始密码;问清要绑定的套餐(已有套餐名,或新建套餐的参数:流量 GB、周期天数、包含哪些节点、是否限速/限设备)。
2. **看现有资源**(只读):
   - `package_list` 看是否已有合适套餐。
   - 需要选节点时 `node_list` 看可用节点及其 ID。
3. **(可选)新建套餐**:`package_create`,参数 `name / traffic_limit_gb / cycle_days / nodes(节点ID数组) / traffic_mode(oneway|twoway) / speed_limit_mbps / device_limit`。记下返回的套餐 ID。
4. **创建用户**:`user_create`,参数 `username / password`(可带 `email / nickname`)。
5. **绑定套餐**：先核对主控实际支持的模型。若同时有 `package_assignment_list` 与 `package_assignment_upsert`，先查询该用户的全部实例，再按本次授权创建或更新指定实例；套餐 ID、起止时间、配额覆盖、主套餐及重置字段按当前 schema 填写，不默认重置已有用量。只有主控采用旧单套餐模型、且本次目标符合该语义时，使用 `package_assign`（`username / package_id`，日期按 schema）。不能在新工具调用失败后自动回退旧接口，也不能混用两种模型。
6. **核对**：`user_detail` 确认账号状态；实例模型使用 `package_assignment_list` 回读套餐、额度、到期时间及 active 状态，旧模型按 `user_detail` 回读。异步凭据下发按工具任务 ID 等待；没有明确期限时最多等待 5 分钟，到期报告未完成而非继续重复创建。
7. **(可选)补 email / 备注**:`user_set_email`、`user_set_remark` 把用户信息补全(都是覆盖式更新,会写入 db)。
8. **生成订阅文件**:`subscribe_file_create`,必填 `username`,可选:
   - `template_id` 或 `template_filename` 绑定 V3 模板(先 `template_v3_list` 看可选模板)
   - `custom_short_code` 自定义短码(字母/数字/_/- 长度 2-16)
   - `selected_tags` V3 模板下的节点筛选标签
   - `remark` 备注
   返回的 short_code 拼成订阅 URL 给用户。`subscribe_file_list` 可复核结果。
9. **交付**:把订阅 URL 告诉用户。如需临时/限时订阅,用 `temp_subscription_create`。

## 注意
- 绑定套餐会异步下发入站凭据,核对前可稍等。
- **订阅文件必须显式 `subscribe_file_create` 创建**——绑套餐本身不会自动生成订阅文件。
- 不要把密码明文回显在公开频道。
- 若 `user_create` 报用户名已存在,改用其它用户名或先 `user_list` 核对。
- 改订阅模板/筛选标签可用 `subscribe_file_update` 在线调整(无需重建)。
