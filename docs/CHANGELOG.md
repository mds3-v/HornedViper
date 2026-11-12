# Changelog

## v1.0 · 2026-10-25

- v1.0 发布里程碑：CTF 调度、报告导出、流量分组与完成度校验
- 删除历史源码备份目录

## v0.5.26 · 2026-10-25

- Traffic grouping template fix


## v0.5.25 · 2026-10-25

- Traffic grouping migration fix


## v0.5.24 · 2026-10-25

- Traffic grouped by task and conversation


## v0.5.23 · 2026-10-25

- Concise paginated report tab


## v0.5.22 · 2026-10-25

- Task report tab


## v0.5.21 · 2026-10-25

- Restricted report export agent


## v0.5.20 · 2026-10-25

- LLM-judged ambiguous start responses


## v0.5.19 · 2026-10-25

- Start 409 max-active no longer counts as active


## v0.5.18 · 2026-10-25

- Idle worker reporting


## v0.5.17 · 2026-10-25

- Platform completion reconciliation every 30s


## v0.5.16 · 2026-10-25

- Reconcile active workers; periodic dispatch tick


## v0.5.15 · 2026-10-25

- Worker failure context per target


## v0.5.14 · 2026-10-25

- Immediate completion lock at submit response


## v0.5.13 · 2026-10-25

- Completed challenge lock (no reopen, no further submits)


## v0.5.12 · 2026-10-25

- Parse platform completion facts; Planner-authorized close


## v0.5.11 · 2026-10-25

- Planner-owned close_target decision


## v0.5.10 · 2026-10-25

- Expected flag count fallback and hash-tracked accepted flags


## v0.5.9 · 2026-10-25

- Direct platform close on solved target


## v0.5.8 · 2026-10-25

- Auto-close completed targets through a fast close Intent


## v0.5.7 · 2026-10-24

- Cross-task artifact/DB access blocked in Worker and ingestion


## v0.5.6 · 2026-10-24

- Cross-task Worker/chat isolation
- Task-scoped assets


## v0.5.5 · 2026-10-24

- Task-scoped asset listing and asset_tasks migration


## v0.5.4 · 2026-10-24

- Merger submits newly assembled/unsubmitted flags after info merge


## v0.5.3 · 2026-10-24

- Swarm/shard lane flags submit immediately; merge handles information only
- Mutex-serialized cross-lane flag submission


## v0.5.2 · 2026-10-24

- Submit guard: require challenge target and active container unless static
- Flag attribution cleanup and container start/close guard
- MaxWorkers=4 (3 normal + 1 fast)


## v0.5.1 · 2026-10-24

- MaxWorkers=4: 3 container-capable workers + 1 fast submit/verify worker
- Container start/close guard; structured challenge flag attribution


## v0.5.0 · 2026-10-24

- Target strategy defer / cooldown / exhausted policy
- Structured Intent target_key, strategy_tag, expected_flags
- Semantic fact fingerprints and target-state UI
- Single-flag completion and pre-submit dedupe
- Fast worker slot, priority aging, partial result retention

# 变更记录

> 只记录对后续开发有影响的变更。详细状态见 `docs/PROGRESS.md`。

## 2026-10-21

### 工具管理

- 新增 `/tools` 工具管理页
- 支持按 scope 启用/禁用内置工具
- 支持新增、编辑、移除、恢复工具
- 支持自定义 HTTP 工具
- 工具页移除「新增分类」入口
- Agent 表单改为按分类多选工具（不再手写 JSON）
- 设置持久化到 `managed_tools` / `tool_categories`
- 会话、任务对话、Worker、Planner 均按设置过滤

### 会话

- 新增独立 `session_agent`，不污染只读的 `mainagent`
- 新会话支持推理等级 `Off / Low / High / Max`
- 新会话支持操作权限 `仅可查看 / 工作区内修改 / 完全权限`
- 权限模式持久化到 `conversations.permission_mode`
- 新会话支持停止生成
- 思考过程改为流式写入 DB，DSH 风格时间线
- 思考默认收起；轮询刷新时保留展开状态和滚动位置
- 会话消息改为局部轮询，不再整页刷新

### 工具

- 新会话增加 Bash / Python / Read / Write / Edit / Glob / Grep / LS
- 增加 `read_image`（尺寸 / 格式 / OCR）
- 增加 `web_search` / `web_fetch`
- 增加 `todo_write`
- 增加后台任务 `job_list / job_output / job_kill`
- Python 支持 `run_in_background`
- 前台运行中的工具也会显示在后台任务面板
- 移除工具调用轮次上限

### 任务

- mainagent 恢复只读设计，执行型工具只属于 session_agent
- Planner 超时改为 5 分钟
- Worker 仍为 20 分钟
- 任务对话改为局部轮询，停止生成
- 任务 worker 保持 HTTPRequest / 证据 / finding 工具链

### 模型与上下文

- 对话 / 任务 / planner / worker 的 MaxTokens 统一为 256000
- Planner / Worker compaction：200000 / 8000 / 8
- 对话仍无自动 compaction

### LLM 记录

- `llm_calls` 增加 `conversation_id`
- 新会话的 session_agent 调用写入 LLM 记录
- LLM 记录页显示「会话 #id」并链接到会话

### 解题溯源

- 任务页新增「解题溯源」Tab
- 按题目分类，不再按 Work 分类
- 题目标签按已获得 flag / 进行中 / 失败着色
- 模型调用和工具轨迹每页 5 条
- 展示系统提示词、模型调用、工具列表和工具轨迹
- 新增 `/llm-calls/{id}` 完整调用详情页
- 支持搜索和只看错误

### Phase 3 蜂群核心

- swarm Intent 创建多个 lane Works
- lane strategy 支持具体 lane brief
- shard 按 host/asset 分组创建 lane，资产不重复分配
- shard 支持 min_success 部分完成
- quorum / min_success 达到后提前取消排队 lane
- lane 独立结果回写和 quorum 合并
- Planner 聚合唤醒
- 会话页 Swarm / Lane 状态

### Phase 2

- 任务探索图 / 资产锚点可视化
- 启动自动恢复活动任务
- 过期租约回收并重新排队 Intent
- `max_minutes` 真正生效
- Dashboard 成功率 / 平均完成时长

### 任务管理

- 任务详情支持永久删除
- 删除前取消运行中的引擎并等待停止
- 删除任务、事件、Work、LLM 记录、聊天、Flag、流量和 artifacts 目录

### 会话上下文

- 新会话增加自动滚动摘要（`conversation_summaries`）
- 超过 22 条消息后压缩较早内容，保留最近 10 条
- 压缩调用也写入 LLM 记录

### Flag 与完成条件

- CTF 任务新增 Flag Tab，自动从发现/事实提取 flag
- 支持手动记录和删除 flag
- 修复空标题静默保存为「新任务」；任务支持重命名
- 移除硬编码验收条件；改由 Planner 从用户目标文本动态推断完成条件

### 任务创建

- 新建 CTF 任务移除必填「CTF 配置」字段
- 目标中包含地址/Token/题目 ID 时自动解析并写入任务预算
- Planner 先做低风险侦察，确认比赛/题目可访问后再安排解题

### 日志与运维

- 日志持久化 SQLite，跨重启保留
- 页面显示最近 500 条，DB 保留 50000 条
- 服务重启时清理遗留 `thinking` 占位消息
- 新增 `docs/` 工程文档

### 修复

- 会话消息左右对齐
- 任务对话底部工具栏模型名/发送按钮靠左
- 思考到 60 步被截断（已移除 display cap）
- 长思考过程中页面整页刷新导致无法查看
- 新会话工具调用 8/40 轮硬截断
- 思考中展开状态被轮询重置

## Phase 4

- Lane 失败重试
- Lane 暂停 / 恢复
- Lane 多次 attempt
- Lane 级审计

### Phase 3 收尾

- 多 flag finding 按 flag 独立 quorum 和提交
- shard 资产覆盖汇总
- 计划页显示 execution_mode

### Quarantine

- 失败、迟到、未确认 flag 的 lane 结果持久化到 `quarantine_items`
- Planner packet 提供 quarantine digest
- 任务会话页展示隔离结果和原始 payload

### P0 收尾

- Live LLM swarm / shard smoke tests
- shard asset ownership validation
- explicit LanePolicy

### P1 Quarantine

- Planner 可列出并读取隔离结果
- 隔离结果支持状态更新

### P1 Lane detail

- Lane 详情页与任务页 lane 跳转

### P1 swarm_lead

- swarm_lead 生成具体 lane plan
- live LLM lead smoke test

### Planner / lane plan contract

- Planner 必须提供完整 strategies 或显式 lane_planning=lead
- 不完整 lane plan 不再补默认值，直接 blocked

- Partial planner lane plans are repaired once by swarm_lead

- Repeated swarm planning failures pause the task

- Quarantine promotion creates evidence artifacts

- Lane artifacts and planned_by audit metadata

- Bounded drain window before swarm merge

- Container admission slot limit

- Soft-cancel running lanes at drain deadline

- Container slot UI on plan page

- Cross-challenge flag conflict quarantine and conflicts UI

- Manual conflict resolution and live conflict review test

- Finding observation inconsistency detection

- Every swarm Intent is now planned by swarm_lead; Planner strategies are ignored

- Planner strategies are passed to swarm_lead as advisory hints

- max_minutes applies per Intent; hard_max_minutes is the whole-task cap
- Direct HTTP flag submissions are deduplicated and recorded

- max_minutes limit removed; only explicit hard_max_minutes can stop the whole task

- MaxWorkers=3, fast submit slot, priority aging, worker/LLM timeouts, partial results, target retry cap

- Target progress accounting: no-progress failures only; partial progress and new evidence reset attempts

- Target retry policy: cooldown after 3 no-progress failures, exhausted after 5, manual recovery

- Planner target states and content-hash progress dedupe

- Structured intent targets, semantic fact fingerprints, target-state UI, and single-flag completion

- Target strategy defer policy, structured target metadata, semantic fact dedupe, single-flag completion

- Container start/close guard and structured challenge flag attribution

- MaxWorkers=4: 3 container-capable workers + 1 fast worker

- Submit safety: require a target and active container unless explicitly static

- Lane flags are submitted immediately; merge quorum governs information only

- Merger submits newly assembled or previously unsubmitted flags

- Task-scoped asset listing via asset_tasks ownership join

- Cross-task isolation for Worker artifacts and chat asset context

- Cross-task Worker/chat isolation and ingestion guard

- Auto-close completed targets through a fast close Intent

- Direct challenge close after target completion, with fast-Intent fallback

- Infer expected_flags from flag_count facts; hash-track accepted flags

- Planner-owned target close decision; Engine validates and executes

- Parse platform completion facts and allow Planner close

- Completed challenges cannot be reopened or submitted again

- Record challenge completion immediately at submit response; block later start/submit

- Worker failure context injected per target

- Reconcile active workers and add periodic dispatch tick

- Platform completion reconciliation and completion error logging

- Idle worker reporting to Planner after 5 minutes

- Fix max-active 409 being treated as an active container

- LLM-judged ambiguous start responses; removed message hardcoding

- Add restricted report export agent and `/导出报告` command

- Add 报告 tab to task detail

- Report tab shows vulnerability type/description/asset with 8-row pages

- Group traffic history by task and conversation; paginated group detail

- Traffic schema migration for conversation-scoped rows

- Retain task traffic and task LLM audit records after task deletion

- Retain conversation traffic and conversation LLM audit records after conversation deletion

- Archive LLM records on task/conversation deletion; retain traffic
