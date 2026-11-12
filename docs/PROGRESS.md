# 进度与路线图

> 快照时间：2026-10-22
> 这个文档回答三个问题：做了什么、现在能做什么、下一步做什么。

## 1. 当前阶段

```text
Phase 2B 已完成
Phase 3A 会话体验与工具能力 已完成
Phase 2 图 + 追溯 + 恢复 已完成
Phase 3 并行 + 蜂群 + 资产分片 核心已实现（早停 / swarm_lead / 覆盖 UI 待补）
Phase 4 Lane 生命周期与韧性（重试 / 暂停恢复 / 多 attempt / 审计）待开始
Phase 3B 可观测性 / 工具管理 / Flag 记录 已完成
```

系统已经能真实调用 DeepSeek 完成一次完整的 CTF 工作流，并具备较完整的单用户 UI。

## 2. 已完成

### 2.1 核心工作流

- `Task → Goal → Intent → Work`
- Planner 是唯一 Intent 生成者
- CTF 全自动审批；Pentest 风险分级审批
- Worker lease / 并发 / 取消
- Goal met / Task completed
- FakeRuntime 与 NormaRuntime
- 事件、审批、发现、资产、证据持久化

### 2.2 模型与记录

- DeepSeek 真实模型闭环
- Provider 配置：DeepSeek / OpenAI / Anthropic / Zhipu
- LLM 调用记录（任务 + 新会话）：
  - request / response
  - thinking
  - tool_calls
  - usage
  - latency / error
- Token 统计 Dashboard

### 2.3 UI

- 仪表盘、任务、发现、资产、审批、事件、流量、Agent、规则、设置
- 响应式布局
- Markdown 安全渲染
- 日志页 SSE 实时 + 自动滚动
- 任务页会话/追踪
- LLM 记录页

### 2.4 日志

- `log.Printf` → loghub → SSE
- 持久化到 SQLite `logs`
- 跨重启保留
- 页面显示最近 500 条，DB 保留 50000 条
- HTTP 请求、任务生命周期、工具、审批、聊天错误等日志

### 2.5 文件

- 任务创建上传多文件/文件夹
- 文件夹保留相对路径
- 会话附件：粘贴、拖拽、文件卡片
- 附件路径校验和下载
- 单文件大小无上限
- 任务附件路径注入 worker prompt

### 2.6 新会话

- 左侧「新会话」
- 历史会话列表
- 独立 `session_agent`
- 推理等级：Off / Low / High / Max
- 操作权限：仅可查看 / 工作区内修改 / 完全权限
- 流式思考过程，DSH 风格时间线
- 思考默认收起，展开状态在轮询中保持
- 停止生成
- 后台任务面板
- Bash / Python / 文件 / Web / read_image / todo / jobs

### 2.7 任务对话

- 保持原始 mainagent 只读设计
- 任务状态/发现/事实/资产/Work 查询
- 局部轮询，不再整页刷新
- 停止生成
- 附件显示
- 可用只读工具：
  - get_work_trace
  - search_task
  - get_llm_call
  - read_attachment

### 2.8 解题溯源

- 任务页新增「解题溯源」Tab
- 按题目查看每个 CTF/任务的解题过程
- 每次模型调用展示：系统提示词、角色、模型、tokens、延迟、工具列表
- 查看/跳转完整请求、响应、思考、工具调用
- 题目状态颜色：已获得 flag / 进行中 / 失败
- 工具轨迹按题目关联，支持搜索、只看错误
- 模型调用和工具轨迹各按 5 条分页
- LLM 调用详情页 `/llm-calls/{id}`

### 2.8 Phase 4 Lane 生命周期与韧性（待开始）

当前 Phase 3 只实现“一个 lane 一个 Work”：

```text
Intent
  └─ Lane Work
       └─ Worker session
```

Phase 4 将把 lane 从 Work 上的标记升级为独立生命周期实体：

```text
Intent
  └─ LaneSpec
       ├─ lane_id / strategy / brief / scope
       └─ WorkAttempt(s)
            ├─ attempt_no
            ├─ worker_id
            └─ status / audit
```

Phase 4 范围：

1. lane 失败重试
2. lane 暂停 / 恢复
3. lane 多次 attempt
4. lane 级审计

这些能力不进入 Phase 3，当前实现保持“一 lane 一 Work、失败不自动重试、不可恢复、无 attempt 历史”。

### 2.9 Phase 3 蜂群核心

- `swarm` Intent 创建多个 lane Works，共享同一 `intent_id`
- lane 独立执行、独立结果回写（`swarm_lane_results`）
- lane 全部结束后 Merger 去重 / 合并 / quorum
- 成功 lane 数不足 quorum 时 Intent failed
- Flag 按 lane 命中数过滤
- `swarm.merged` 聚合事件唤醒 Planner
- lane strategy 支持对象 `{name, brief}`，每个 lane 有具体方法/目标/成功信号
- 任务会话页展示 Swarm / Lane / Lane Brief
- 单元测试 `TestSwarmCreatesLanesAndMerges`
- `shard` Intent 按资产分组创建 lane，lane 只接收自己的资产子集
- 同一 parent/host 资产不拆到不同 shard
- 默认所有 shard 成功，`min_success` 可允许部分完成
- 单元测试 `TestShardCreatesDisjointLanesAndMerges`
- 多 flag finding 按单个 flag 独立 quorum/提交，`confirmed_flags` 只统计确认通过的 flag
- 单元测试 `TestSwarmSubmitsOnlyConfirmedFlagsFromMultiFlagFinding`
- 真实 DeepSeek Worker 的 swarm / shard live smoke test（`HV_LIVE_LLM=1`）
- shard 显式 `asset_ids` 校验任务归属和 scope
- 显式 `LanePolicy`：CTF / generic 分流
- Planner `list_quarantine` / `read_quarantine` 工具
- 隔离结果支持标记复核 / 忽略 / 重新打开
- 隔离结果可提升为 quarantine evidence artifact；不自动提交 flag
- Lane 详情页展示 artifacts
- `planned_by` 记录 `swarm_lead`（shard 由 ShardCoordinator 分配，不走 lead）
- 计划页展示 strategy / lane brief / lane 资产数量
- 达到 quorum 后提供 DrainWindow（生产 5m）等待 running lane，超时后冻结合并
- MaxContainers admission 限制 lane 容器槽位；尚无容器启动/回退生命周期
- DrainWindow 到期 soft-cancel running lane context；当前工具不会被强制中断
- 计划页显示容器槽位 Active/Max
- 冲突检测：同一 flag 跨 challenge，写入 swarm_conflicts
- swarm_lead 冲突建议；CTF 自动 quarantine，不进入人工等待
- FindingSpec 增加 subject_key / case_key / observation
- 同 subject + 同 case 的 observed/not_observed 冲突检测
- 不同策略/不同 case 的结论差异不视为冲突
- 任务页新增「冲突」Tab；支持手动保留 / 隔离 / 人工复核
- HTTPRequest 直接提交 flag 时拦截重复提交，并记录 accepted 结果
- 真实 LLM conflict review smoke test
- 计划页显示 lane strategy / brief
- Lane 详情页：`/tasks/{id}/lanes/{workID}`，展示 lane brief、调用、活动、结果、隔离
- 计划页显示 lanes / quorum / min_success / lane 状态
- `swarm_lead` 在 Planner 显式 `lane_planning=lead` 时生成完整 lane plan
- 部分 strategy 触发一次 swarm_lead repair，保留已有 lane、补齐缺失 lane
- repair 失败才 block；不再补默认值
- 连续 3 次 swarm planning failure 后任务 paused（planning stalled）
- 完整 strategies / 显式 lane_planning=lead 仍支持
- 失败 / 迟到 / 未确认 flag 结果写入 `quarantine_items`，原始 payload 保留
- Planner packet 带最多 5 条 quarantine digest
- 任务会话页展示隔离结果，可展开原始 payload
- 单元测试 `TestFailedSwarmLaneIsQuarantined`
- shard 蜂群卡片显示资产覆盖数和未完成 lane
- 计划页显示 Intent 的 `execution_mode`
- 达到 swarm quorum / shard min_success 后取消尚未启动的 lane，直接合并

### 2.9.1 Phase 3 未完成项

- `swarm_lead` 只有 Agent 配置种子，还没有运行时调用和冲突裁决协议
- `read_quarantine` / `list_quarantine` 工具尚未接入 Planner，目前只提供 digest
- quarantine 的 promote / dismiss 还没有 UI/审批流程
- ~~shard 覆盖统计没有落库/UI~~ 已补充覆盖汇总（covered/total/failed lanes）
- lane 级详情页/追踪入口缺失：只能看汇总表，不能从 lane 跳到该 lane 的 calls/tools/artifacts
- `CTFLanePolicy` 尚未显式化：当前 swarm/shard 默认使用 CTF 语义，没有 `task.Mode` 分支
- 同一 flag 的证据矛盾还没有复核入口
- 没有真实 LLM 的 swarm/shard 端到端测试，目前只有 FakeRuntime 单测
- MaxWorkers 与 CTF 容器槽位没有联动
- shard 显式 `asset_ids` 还没有验证是否属于当前任务
- ~~Planner 计划页没有展示 `execution_mode`~~ 已补充模式列；具体 lane 规划仍未展开

### 2.10 Phase 2 收尾

- 任务「探索图」Tab：目标 / 意图 / 事实 / 发现四层图，锚点资产表
- 启动时自动恢复上次运行中的任务
- 30 秒周期回收过期 Work 租约，Intent 重新排队
- `max_minutes` 限制已移除；只有显式 `hard_max_minutes` 才是 Task 硬截止
- Dashboard 增加任务成功率与平均完成时长

### 2.11 可观测性与管理

- 任务支持永久删除（运行中会先取消，等待停止后删除）
- 任务页「解题溯源」：按题目分类，题目状态着色
- 模型调用每页 5 条，系统提示词 / 思考 / 工具调用可追溯
- LLM 调用详情页 `/llm-calls/{id}`
- Flag Tab：自动从 findings/facts/submissions 同步，支持手动添加
- 工具管理：DSH 风格卡片、按 Agent 分类、内置/自定义 HTTP 工具、分类与 CRUD
- Agent 表单：按分类多选工具，不再手写 JSON
- 新会话 / 任务对话 / Worker / Planner 的 LLM 调用统一记录
- 新会话自动滚动摘要（22 条后，保留最近 10 条）

### 2.12 会话上下文压缩

- 新会话超过 22 条已完成消息后自动滚动摘要
- 保留最近 10 条原始消息
- 摘要持久化到 `conversation_summaries`
- 后续请求注入摘要并跳过已压缩消息

### 2.13 Flag 记录

- CTF 任务新增「Flag」Tab
- 自动从发现 / 事实中提取 flag 和题目编号
- 自动镜像已提交的 flag
- 支持手动添加、状态修改、删除

### 2.14 工具管理

- 左侧「工具管理」页面
- 39 个内置工具按会话 / 任务主 Agent / Worker / Planner 分组
- 支持搜索、启用/禁用
- 新增/编辑/移除工具
- Agent 表单改为按分类多选工具，工具清单来自工具管理
- 工具页不再提供「新增分类」入口
- 自定义 HTTP 工具（URL / 方法 / 请求头 / 请求体，`{input}` 替换）
- 已移除工具可恢复
- 开关和元数据持久化到 `managed_tools` / `tool_categories`
- 核心工具不可关闭或删除
- 修改对新请求生效，不中断正在运行的任务

### 2.15 CTFd

- 自动 CTF：拉取未解决题目
- 为每题创建 Task
- 新建 CTF 任务不再强制填写 CTF 配置；可直接写在目标中，系统自动识别
- 每题独立 CTF 配置
- `external_tasks` 去重
- Finding 中的 flag 自动提交
- 自然语言 CTF 配置解析
- 支持 `flag{...}` / `FLAG{...}` 等格式
- 暂不支持容器启动/停止

### 2.16 工具

已实现：

```text
Bash
Python
Read / Write / Edit / MultiEdit
Glob / Grep / LS
read_image
web_search / web_fetch
todo_write
job_list / job_output / job_kill
read_attachment
HTTPRequest（Task worker）
```

工具调用轮次：不设上限。

## 3. 当前能力边界

| 能力 | 状态 |
|---|---|
| 单任务完整闭环 | 已完成 |
| 真实模型 | 已完成 |
| 单用户 UI | 已完成 |
| 多任务并行 | Worker pool 支持 |
| 新会话工具 | 已完成 |
| 自动上下文压缩（会话） | 已完成 |
| 自动上下文压缩（任务） | Norma compaction |
| CTFd 自动拉题 | 已完成 |
| CTFd 容器启动/停止 | 未完成 |
| rCTF / 其他平台 | 未完成 |
| Swarm / Shard | 核心已实现（CTF 语义） |
| 多 Agent UI 编排 | 未完成 |
| 生产级鉴权 | 未完成 |

## 4. 已知问题

### P0

1. **workspace-write 不是 OS 沙箱**
   - Write/Edit/MultiEdit 有路径校验
   - Bash/Python 仍可访问本机文件系统
3. **Web Search 强依赖 Bing**
   - DuckDuckGo 在部分网络不可达
   - 搜索质量受网络和查询影响
4. **CTFd 没有容器生命周期**
   - 标准 CTFd API 不统一
   - 需要插件级适配

### P1

2. 后台 job 列表不跨重启
   - `tool.Manager` 在 Server 内存
   - 输出文件仍在磁盘
3. 自动 CTF 不是后台定时轮询
4. 任务 worker 没有 WebSearch / read_image
5. `max_minutes` 限制已移除；`hard_max_minutes` 可选
6. 日志页面只显示最近 500 条
7. 会话附件没有在任务概览中展示（仅消息内）
8. `workspace-write` 的 Bash/Python 命令级限制不完整

### P2

- 会话搜索 / 归档 / 分组
- 文件预览（PDF、图片缩略图）
- 更细的 Agent 工具配置（Agent 表 tools_json 目前未驱动运行时）
- 会话级模型选择
- LLM 流式 token 级渲染

## 5. 下一步优先级

### P0：稳定性

1. 把 workspace-write 做成真正的沙箱（macOS sandbox-exec 或容器）
2. 自动 CTF 定时轮询
3. CTFd / Benchmark 容器生命周期

### P1：CTF 实战

1. CTFd 容器适配（插件）
2. 给 worker 增加 WebSearch / read_image
3. 题目附件自动下载 / 挂载
4. Flag 提交结果 UI
5. 多 flag 题目支持

### P2：多 Agent

1. Swarm：单 Intent 多 lane
2. 共享黑板
3. quorum / 合并
4. Agent 工具配置真正生效

## 6. 重要决策记录

| 决策 | 原因 |
|---|---|
| mainagent 只读 | 保留原始解释者角色，避免任务对话变成执行器 |
| session_agent 独立 | 新会话需要工具能力，但不污染 mainagent |
| 每题独立 CTF 配置 | 不同题目可能属于不同平台/Token |
| 不设工具轮次上限 | 长任务需要持续迭代；用时间/用户停止收敛 |
| 思考过程流式写 DB | 轮询可看到实时思考；无需 WebSocket |
| 默认思考收起 | 减少消息高度；用户可展开 |
| 局部轮询代替整页刷新 | 保持滚动、展开状态、输入内容 |
| 权限等级持久化 | 会话恢复后保持操作边界 |

## 7. 验证清单

```bash
go test ./...
go vet ./...
```

页面：

```text
/
/tasks
/tasks/4?tab=sessions
/findings
/approval-records
/new
/conversation/4
/chat?task=4
/logs
/llm-records
/settings
```

功能：

- 新会话发送消息，思考流式显示
- 思考展开后轮询不收起
- 停止生成
- 后台 `sleep` 任务显示/输出/停止
- 任务对话不整页刷新
- 任务对话保持只读
- 会话权限选择持久化
- `go test ./...` / `go vet ./...` 通过

## Worker scheduling / recovery (2026-10-24)

- `MaxWorkers=4`（3 个普通 Worker + 1 个 fast Worker）
- 预留 1 个 fast slot 给 submit / verify / finalize
- Intent priority aging（默认 5 分钟 +1）
- `WorkerTimeout=20m`
- 单次 LLM 调用 `LLMTimeout=2m`
- Work partial result 保留
- 单 target 记账第一层：no_progress 才增加 Attempts；partial_progress / new_evidence 清零；accepted flag 标记 Solved
- 单 target 失败 2 次自动升级 swarm
- 失败 3 次进入 10 分钟 cooldown，降优先级
- cooldown 后需要新的 `strategy_tag`
- 失败 5 次 exhausted，停止自动重试
- 人工恢复：`POST /api/tasks/{id}/targets/{key}/recover` 或 `budget.manual_recover=true`
- `PlannerPacket.target_states` 暴露 attempts/cooldown/exhausted/reason
- progress 用 content hash，重复 fact/finding/asset/evidence 不计 progress

## Structured targets / failure context (2026-10-24)

- IntentSpec / Intent 增加 `target_key`、`strategy_tag`、`expected_flags`
- PlannerPacket 增加 `TargetStates`、`RecentFailures`
- 计划页显示 cooldown / exhausted / 最后失败原因 / 恢复按钮
- fact progress 使用语义指纹，`port 80 open` 与 `80/tcp is open` 归一
- 已 accepted 的 flag 在调用 submitter 前直接跳过
- `expected_flags=1` 且 flag accepted 后，取消同 target 的其他 Intent

## Target policy v2 (2026-10-24)

- `IntentDeferred` state
- 同 target + 同 strategy + 无新证据 → defer
- 同 target + 不同 strategy → 保持 approved
- submit / verify / finalize → fast slot，不冻结
- target solved → 取消同 target 其他 Intent
- target exhausted → 非 fast Intent 自动 defer，人工可恢复
- `IntentSpec.target_key / strategy_tag / expected_flags`
- `PlannerPacket.TargetStates / RecentFailures`
- Fact 语义去重（token 归一）+ content hash target progress
- 单 flag（expected_flags=1）accepted 后取消同 target 其他 Intent

## v0.5 scoring fixes (2026-10-24)

- TaskFlag attribution uses structured Intent.TargetKey; invalid text labels are filtered
- `SyncTaskFlags` repairs/deletes legacy arbitrary challenge labels
- Runtime container guard blocks repeated start of active challenge
- Runtime container guard blocks close before the challenge is completed

## Submit safety (2026-10-24)

- Runtime refuses submit without `unique_code`
- Runtime refuses submit when challenge has no active container or is closed
- `budget.container_required=false` is the explicit static/offline bypass
- Completed challenges remain available for idempotent verification

## Immediate lane flag submission (2026-10-24)

- single / swarm / shard 都立即提交 lane 找到的 flag
- quorum / min_success 只决定信息合并，不阻断 flag 提交
- 跨 lane 重复 flag 由 `submissionMu` + FlagAccepted 串行去重

## Merger flag submission (2026-10-24)

- Lane flags submit immediately
- Merger re-checks merged findings and submits newly assembled / previously unsubmitted flags
- Accepted/duplicate flags are skipped through FlagAccepted + submission mutex

## Task-scoped assets (2026-10-24)

- `assets` remains globally deduplicated
- `asset_tasks` records task ownership
- `RecordAssetForTask` writes ownership
- `ListTaskAssets` powers task asset tab
- startup migration backfills ownership from anchors and asset.recorded events

## Cross-task isolation (2026-10-24)

- Worker sandbox rejects other task artifact paths and host DB access
- Bash/Read/Glob/Grep paths are bounded to the current Work directory
- Chat context uses ListTaskAssets instead of global ListAssets

## Cross-task evidence defense (2026-10-24)

- Runtime sandbox blocks other task artifact paths, `task-N`, and host DB access
- Engine ingestion rejects facts/findings that embed another task's artifact path
- Chat main-agent context uses ListTaskAssets, not the global asset list

## Auto-close completed targets (2026-10-24)

- Planner decides to close via `close_target=true`
- Engine validates accepted/expected flags and calls platform close directly
- if direct close fails, fallback fast close Intent
- close Intent is high priority, fast_lane, target-bound
- worker closes the completed challenge; no Planner round-trip

## Direct challenge close (2026-10-24)

- `platform.Benchmark.Close` calls `/openapi/v1/challenges/close`
- credentials resolve from task.budget.ctf or uploaded BENCHMARK_BASE_URL/TOKEN
- Engine `CloseChallenge` callback closes synchronously after target solved
- fallback remains a fast close Intent

## Expected flag inference (2026-10-25)

- `expected_flags` can come from Planner
- fallback parses facts containing `flag_count=N` / `total_flag_count=N`
- accepted flags are tracked by content hash
- close fires when accepted count reaches expected count

## Close responsibility (2026-10-25)

- Engine no longer auto-closes on flag acceptance
- Planner sees TargetStates and proposes close_target
- Engine validates: accepted evidence + expected_flags before close

## JSON completion facts / Planner close (2026-10-25)

- parse quoted `flag_count` and `is_completed` from platform catalog facts
- `PlatformCompleted` lets Planner close even when accepted flags were not recorded locally
- close remains Planner-authorized via close_target

## Completed challenge lock (2026-10-25)

- `challenge_completions` persists completed challenge ids
- Engine rejects new solve Intents after completion
- Runtime blocks start/submit for completed challenges
- Only close_target may act on a completed target

## Completion recorded at submit (2026-10-25)

- runtime records `challenge_completions` immediately when a submit response reports correct_flag_count == total_flag_count
- subsequent start/submit for that challenge is blocked even before the next Engine result cycle
- exact challenge code is used, not a generic Intent target

## Worker failure context (2026-10-25)

- WorkerRequest includes `Failures` for the current target
- previous failed Work ids, statuses and stop reasons are injected into the worker prompt
- worker prompt forbids repeating the same PoC without new evidence

## Worker re-dispatch reconciliation (2026-10-25)

- Engine reconciles active/fast/container counts from running Works before every dispatch
- 5-second dispatch ticker re-checks queued Intents
- canceled/timed-out/restarted Works no longer leave the in-memory active slot permanently occupied

## Completion reconciliation (2026-10-25)

- Engine calls platform challenge list at startup and every 30s
- is_completed challenges are written to challenge_completions
- target states become Solved/PlatformCompleted and same-target Intents are canceled
- submit/start are blocked by the persisted completion lock

## Idle worker reporting (2026-10-25)

- Engine tracks continuously idle worker capacity
- after 5 minutes it emits `workers.idle`
- PlannerPacket includes active_workers / idle_workers / max_workers
- Planner must use idle capacity or explain why no safe next step exists

## Start 409 semantics (2026-10-25)

- only HTTP 200 start marks a challenge active
- HTTP 409 marks active only for explicit already-active/reuse responses
- max-active-instance-limit 409 no longer permits submit without a container

## LLM container-state judge (2026-10-25)

- ambiguous start responses are classified by the model, not by message-string matching
- judge returns usable/true only when the challenge has a valid answering scene
- judge failure is fail-closed (do not mark active)
- explicit HTTP 200 remains protocol success

## Report export agent (2026-10-25)

- new conversation command `/导出报告 [task_id]`
- report agent exposes only `export_report`; no Bash/HTTP/recon tools
- task page has a direct 导出报告 button
- Markdown reports written to `artifacts/task-<id>/reports/`
- download endpoint `/reports?task=<id>&name=<file>`

## Task report tab (2026-10-25)

- task detail has a 报告 tab rendering the Markdown report in-page
- tab includes a direct Markdown download button

## Concise report tab (2026-10-25)

- report tab is now a vulnerabilities-only table: type, description, asset
- 8 rows per page, paginated
- full Markdown export remains available from the tab

## Traffic grouping (2026-10-25)

- traffic history has separate 任务流量 and 新会话流量 sections
- clicking a group opens its requests with standard pagination (10/page)
- conversation web_fetch calls are recorded with conversation_id and task_id=0
- traffic task_id migration removes legacy foreign key so conversation rows can use task_id=0

## Delete retention rules (2026-10-25)

- task deletion removes task graph/execution rows and files
- task traffic is retained; task LLM calls are archived to `artifacts/llm-archive/` then removed from SQLite
- conversation deletion removes messages/summaries/todos and files
- conversation traffic is retained; conversation LLM calls are archived then removed from SQLite
