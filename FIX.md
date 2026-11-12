# FIX · 跑分与执行可靠性修复清单

> 建立时间：2026-10-24
> 依据：Task #7（v0.3）、Task #9（v0.4_new）的实际跑分数据，以及当前执行链路审计。
> 目标：先阻止“时间被无效重试吃掉”，再补齐可恢复性和完成判定。
>
> 本轮已实现：FIX-00、FIX-01（基础版）、FIX-02（基础版 + 第二层 cooldown/exhausted）、FIX-03。
> 失败记账第一层已实现：no_progress / partial_progress / new_evidence / target_solved。
> 已补充：TargetStates 进入 PlannerPacket、content-hash + 语义 fact 去重、exhausted_reason、UI、ExpectedFlags 单 flag 收尾。
> 已实现策略级 defer：同 target 同 strategy 无进展才延迟，不同 strategy/fast 不冻结。

## 结论先行

v0.4_new 变慢不是 LLM API 变慢：

```text
v0.3     worker LLM 平均 4237 ms
v0.4_new worker LLM 平均 4622 ms
v0.3     planner 平均 8349 ms
v0.4_new planner 平均 7679 ms
```

主要问题是执行层：

```text
24 / 79 个 Work 以 "worker did not call finish" 结束
v0.3 只有 7 / 132
部分 Work 跑了 64.5 / 63.1 分钟
Planner 在 1.5h - 4h 反复 Resume b-02 / b-03
失败后部分结果被丢弃
最终 37/74 flags、12430/23000，平台 terminal 后 goal_met
```

## 本次跑分新发现（Task #9）

### 1. E3-01 已拿到 flag，但提交被饿死

```text
03.1m  fact: e3-01 flag captured
03.2m  intent 1588 proposed: submit e3-01
60.3m  work 201 / intent 1715 actually submits e3-01
395.8m work 250 / intent 1588 finally runs
```

原因不是模型不知道要提交，而是调度：

```text
ClaimNextIntent: ORDER BY priority DESC, id ASC
MaxWorkers = 2
intent 1588 priority = 1
大量新 Intent priority = 2/4
没有 priority aging
长 Work 占满两个 worker
```

结果：

```text
低优先级的 submit intent 被饿死 6.5 小时
另一个高优先级 work 偶然替它提交
```

### 2. 1.5h - 4h 的停滞不是模型慢

```text
2h:  22 flags / 5580
4h:  25 flags / 7080
净增：3 flags / 1500 分
```

同期 v0.3：

```text
2h:  26 flags / 9020
4h:  47 flags / 15920
净增：21 flags / 6900 分
```

原因是：

```text
b-02 / b-03 的 Resume 失败循环
work #208 64.5m
work #209 63.1m
work #213 35.7m
多个 20m no-finish
worker 槽位被占满
submit intent 被饿死
```

### 3. Worker 为什么会没有 finish

两类直接原因：

```text
1. Session 20m timeout
   → result timeout
   → worker 没机会调用 finish

2. 单次 LLM 请求阻塞约 60m
   → model_error: read tcp ... operation timed out
   → MaxDuration 20m 没有覆盖阻塞中的 LLM HTTP 调用
```

证据：

```text
work #208 64.5m  model_error: read tcp ... timed out
work #209 63.1m  model_error: read tcp ... timed out
work #240 64.5m  model_error: read tcp ... timed out
```

另外 work #209 的第一批 Bash 命令直接读取了：

```text
/Users/mds3/Documents/ctf-agent-vibecoding/hornedviper.db
```

这说明 Worker 还能访问宿主工作区和数据库，需要隔离。

### 4. 为什么没有调用蜂群

```text
Task #9 swarm_runs = []
b-02 / b-03 的 Resume Intent 全部是 execution_mode = single
```

当前没有“连续失败自动升级为 swarm”的规则：

```text
Planner 选 single → 就一直 single
连续失败 → 继续 Resume
```

需要在同一 target 连续失败后显式触发：

```text
failure threshold
  → swarm_lead 生成多策略
  → 或标记 target 为 hard，暂停自动重试
```

### 5. 为什么失败 Work 没有保留部分结果

`NormaRuntime.Work` 在 `sess.Prompt` 返回错误时直接：

```go
return nil, fmt.Errorf("norma worker: %w", err)
```

此时 `cap.Result` 中的：

```text
assets / facts / findings / evidence
```

没有被返回。

Engine 的 `onWorkDone` 对 `out.err != nil` 也只标记 Work failed，不落库 partial。

只有正常结束但没有 finish 的路径会返回 blocked result；模型错误/超时路径直接丢弃。

---

## P0 · 立即修复

### [x] FIX-00 Intent 调度优先级老化 / 提交类 Intent 快速通道（已实现）

**现状**

`ClaimNextIntent`：

```sql
ORDER BY priority DESC, id ASC
```

`priority` 数值越大越优先，且没有 aging。

**证据**

- e3-01 flag 在 3.1m 已拿到
- 提交 Intent 1588 priority=1
- 大量新 Intent priority=2/4 持续插入
- 提交实际到 60.3m 才由另一个 Work 完成
- 1588 直到 395.8m 才被 claim

**修复**

- 增加 priority aging：

```text
effective_priority = priority + wait_minutes / aging_interval
```

- 提交/收尾类 Intent 提高优先级，或标记 `urgent=true`
- 同一 target 已发现 flag 但未提交时，禁止继续产生同类高优先级新任务
- 防止低优先级 Intent 无限饿死

**验收**

- priority=1 的 submit 在 5 分钟内被 claim（有空闲 worker 时）
- 高优先级新 Intent 不能无限插队
- 测试：持续产生高优先级 Intent，低优先级仍能获得执行

---

### [x] FIX-01 Work 超时后保留部分结果（已实现基础版）

**现状**

`NormaRuntime.Work` 在 Session 结束但没有 `finish` 时返回：

```text
status = blocked
error  = worker did not call finish
```

`onWorkDone` 将 Work 标记 failed，已经产生的：

```text
assets / facts / findings / evidence
```

不会被记录。

**证据**

Task #9：

```text
24 个 worker did not call finish
```

其中多个耗时 20 分钟，两个耗时 64.5 / 63.1 分钟。

**已实现**

- `WorkResult.Partial`
- `NormaRuntime.Work` 在 Session 错误/超时时返回 partial result，而不是 nil
- Engine 记录 partial facts/findings/evidence，但不自动提交 flag
- `WorkerTimeout` 默认 20m
- `LLMTimeout` 默认 2m（单次模型调用超时）

**仍待**

- partial 的 UI 展示和 quarantine 摘要
- Session 超时/无 finish 时，仍然返回 `cap.Result` 中的四类输出
- Engine 对 partial 结果：
  - assets/facts/findings/evidence 正常落库
  - 标记 `partial`
  - Intent 标记 `blocked` 或 `failed`
  - 不把 partial flag 自动确认
- 在 Lane 详情页显示 partial 原因和已保存内容

**验收**

- fake runtime 返回无 finish + facts，facts 仍落库
- partial flag 不进入 confirmed
- 超时后 quarantine 能看到 partial 摘要
- `go test ./internal/kernel ./internal/runtime`

---

### [ ] FIX-15 Worker 文件系统 / 数据库隔离

**证据**

Task #9 work #209 的 Bash 直接执行：

```text
cd /Users/mds3/Documents/ctf-agent-vibecoding
sqlite3 -readonly hornedviper.db ".schema works"
```

Worker 可以读取宿主项目数据库、其他任务状态和内部表。

**修复**

- Worker 的 Bash 工作目录限制到 task workdir
- 禁止访问 workspace 根目录和 `hornedviper.db*`
- 使用 OS 级 sandbox / 容器
- 对命令做路径和文件描述符限制
- 记录越界访问并终止 Work

**验收**

- Worker 无法读取 host DB
- `cd $PROJECT_ROOT` 被拒绝
- 越界访问产生审计事件

---

### [x] FIX-02 同一目标重试上限与失败感知 Planner（基础版）

> 第一层记账已实现：`targetState{Attempts, ProgressCount, LastProgressAt, LastFailureAt, Solved}`。
> `no_progress` 才增加 Attempts；有 facts/findings/assets/evidence 或 accepted flag 则清零。

**现状**

Planner 可以在同一个 b-02 / b-03 上反复生成：

```text
Resume b-02
Resume b-03
Continue b-03
Re-open b-02
```

没有连续失败上限。

**证据**

Task #9 在 1.5h - 4h 多次重复：

```text
work #206 b-02 20m no finish
work #208 b-03 64m no finish
work #209 b-02 63m no finish
work #213 b-02 35m no finish
work #214 b-02 20m no finish
work #219 b-02 22m no finish
work #228 b-02 20m no finish
```

**已实现**

- Engine `targetState`（第一层 progress 记账）
- 失败 2 次后自动把新的 single Intent 升级为 swarm
- 失败 3 次后进入 10 分钟 cooldown
- cooldown 后只允许新的 `budget.strategy_tag`
- 失败 5 次后 `exhausted`，停止自动重试
- `budget.manual_recover=true` / `POST /api/tasks/{id}/targets/{key}/recover` 人工恢复
- `TargetStates` 写入 PlannerPacket（attempts / cooldown / exhausted / last_failure_reason / exhausted_reason）
- progress 使用 content hash；重复 fact/finding/asset/evidence 不算 progress

**仍待**

- 将 failed_targets / attempt_counts 写入 PlannerPacket
- 持久化 target 账本（当前是 Engine 内存）
- cooldown / 人工解除

**原设计**

- 新增 target-key 失败账本：

```text
target_key
attempts
last_failure_reason
last_evidence_id
cooldown_until
```

- 同一 target 连续失败默认上限：`2`
- 达到上限后：
  - 禁止自动 Resume
  - 必须换 target
  - 或要求 Planner 提供新 evidence / 新策略
- PlannerPacket 增加：

```text
failed_targets
attempt_counts
cooldown_targets
```

- Prompt 明确：不得重复提交无新证据的 Resume。

**验收**

- 同一 target 第三次 Resume 被拒绝
- 拒绝事件写入 workflow_events
- Planner 收到 failed_targets
- 新 target 不受影响

---

### [x] FIX-03 Work 硬超时覆盖长工具调用（已实现）

**现状**

Worker 的 Session `MaxDuration` 是 20 分钟，但 Task #9 出现：

```text
work #208 64.5 分钟
work #209 63.1 分钟
```

说明 20 分钟检查没有覆盖长工具调用/阻塞调用。

**修复**

- 工具调用必须继承 Work deadline
- 每个工具调用有独立 timeout（HTTP 30s 已有，Bash 需补）
- Session 每轮开始检查 `ctx.Done()`
- 任一工具阻塞超过限制：
  - 取消该工具
  - 返回 partial
  - 不等待整段 Session 结束

**验收**

- 模拟阻塞 Bash/HTTP，Work 在 deadline 附近结束
- 不允许出现 60 分钟 Work
- 超时后 partial 结果保留

---

### [ ] FIX-04 Challenge 级时间片与无进展降级

**现状**

一个困难 challenge 可以占用大量 Worker 时间，失败后 Planner 又继续投入。

**修复**

- 每个 target 维护：
  - `first_seen_at`
  - `last_progress_at`
  - `attempts`
  - `accepted_flags`
- 超过 `target_max_minutes` 或无新 evidence 超过 `stale_minutes`：
  - target 降级
  - 暂缓新 Intent
  - 让位给未覆盖 challenge
- 不把 target timeout 升级为 Task timeout

**验收**

- 一个 target 卡住时，其他 target 继续执行
- PlannerPacket 包含 stale/failed targets
- Task 不会因单个 target 卡住而结束

---

### [ ] FIX-05 动态验收，不以平台 terminal 代替目标完成

**现状**

Task #9 在平台返回 409 `task already finished` 后，Planner 以：

```text
remaining flags unobtainable
```

提出 goal_met，Engine 只校验 evidence 归属，于是接受。

结果：

```text
37/74 flags
12430/23000
status=completed
```

**修复**

- 恢复动态验收合同，而非固定字符串：
  - Planner 在任务开始时根据 Goal.text 生成 acceptance
  - 存入 Goal
  - goal_met 时必须覆盖 acceptance
- 平台 terminal 只能证明“不可继续”，不能证明“目标已完成”
- 未覆盖 acceptance 时：
  - goal_met 被拒绝
  - task 标记 `partial` / `timed_out`
  - 保留 score 和 flags
- 不允许 LLM 自己降低验收标准

**验收**

- 37/74 flags 时 goal_met 被拒绝
- 全部 flag accepted 时 goal_met 通过
- 平台 terminal + 未完成时状态为 partial，不是 completed

---

## P1 · 收尾必须补齐

### [ ] FIX-06 软预算 / 硬预算 / checkpoint

**背景**

`max_minutes` 已移除。当前只有显式 `hard_max_minutes` 才停止整个 Task。

长题需要：

```text
soft_minutes
hard_minutes
checkpoint / resume
```

**修复**

- Task：
  - `default_intent_minutes`
  - `max_intent_minutes`
  - `hard_max_minutes`（可选）
- Intent：
  - `soft_minutes`
  - `hard_minutes`
- soft 到期：触发预算审查，不杀 Work
- hard 到期：timed_out + partial + quarantine
- 引入 checkpoint，避免 Resume 从头开始

**验收**

- 3 小时题不会被 1 小时默认值杀掉
- soft 到期后 Planner 能选择延长/拆分/暂停
- hard 到期后结果保留

---

### [ ] FIX-07 直接 HTTP flag 提交的完整去重与回填

**已做**

- `HTTPRequest` 识别 `/submit`
- 发送前检查 accepted 记录
- accepted 后写入 `flag_submissions`

**仍需**

- 覆盖 `/challenges/flags` 等其他提交路径
- 识别 `409 duplicate` 并回填 accepted
- 历史 Task #7 / #8 / #9 的 accepted 提交从 traffic 回填到 `flag_submissions`
- Flag Tab 显示 `accepted`，而不是一直 `found`

**验收**

- 重复提交不产生网络请求
- 历史 accepted 可回填
- 本地 accepted 数与平台一致

---

### [ ] FIX-08 CTFd / 平台容器生命周期

**现状**

`MaxContainers` 只是 admission，不是实际容器生命周期。

**修复**

- `container_leases` 状态机：
  - reserved → starting → ready → busy → draining → released
  - starting → failed
- 启动失败：
  - 释放 reservation
  - lane 失败/quarantine
  - 不消耗 quorum
- 孤儿容器 reaper
- UI 显示 active/max/lease

**验收**

- 启动失败不占槽位
- Work 结束释放 lease
- 取消/暂停释放容器

---

### [ ] FIX-09 Pentest action ledger / action 冲突

**现状**

没有 action ledger；Pentest action 冲突无法判定。

**修复**

- `pentest_actions`：
  - action_key
  - target
  - effect
  - state
  - before/after evidence
  - lock_id
- 同 action_key 不同终态 → `action_result_conflict`
- 冲突只 hold 受影响 target/action
- 不自动重试 exploit

**验收**

- 同 action_key 多 lane 状态冲突被记录
- 不自动重放破坏性动作
- 其他 target 继续

---

### [ ] FIX-10 工具级 drain

**现状**

只有 Work context cancel，工具可能仍在执行。

**修复**

- Runtime `DrainController`
- drain 后不再接受新工具调用
- 当前工具完成或到 checkpoint
- 可逆动作等待；破坏性动作到安全点
- 超时才强制取消
- 记录 indeterminate

**验收**

- drain 期间无新工具调用
- 当前工具不被 mid-flight 随意 kill
- 容器/lease 释放

---

### [ ] FIX-11 quarantine promote 审批

**现状**

`promote` 直接生成 evidence。

**修复**

- `quarantine_approvals`
- pending → approved / rejected
- approve 只生成 candidate evidence
- 不提交 flag，不改变 quorum

**验收**

- 未审批不能 promote
- approve/reject 审计
- 审批不自动提交 flag

---

### [ ] FIX-12 真实 CTF 端到端与 live shard 测试

**现状**

Live shard smoke 使用假域名，模型经常 blocked。

**修复**

- 启动本地可控 HTTP 服务
- 或使用真实可达的 shard 资产
- 验证：
  - shard 分配
  - worker 实际请求
  - min_success merge
  - flag/evidence 落库

**验收**

- live shard 测试稳定通过
- 不能依赖模型是否愿意访问假域名

---

## P2 · 后续增强

### [ ] FIX-13 LaneSpec / WorkAttempt / lane 级重试

- 独立 `lane_specs` 表
- `work_attempts` 表
- lane 重试、pause/resume、多 attempt
- lane 级审计

### [ ] FIX-14 版本控制与跑分基线

**现状**

项目没有 Git 历史，无法准确回溯 v0.3 源码。

**修复**

- `git init`
- 每次跑分前打 tag：

```text
v0.3-run
v0.4-run
```

- 备份源码和 DB 快照
- 跑分记录：

```text
tag
commit
task_id
start/end
model
budget
accepted flags
score
```

---

## 修复顺序建议

```text
1. FIX-01 partial result
2. FIX-02 retry cap / failed_targets
3. FIX-03 tool timeout
4. FIX-04 target time slice
5. FIX-05 dynamic acceptance
6. FIX-06 soft/hard budget + checkpoint
7. FIX-07 submission backfill
8. FIX-08 container lifecycle
9. FIX-09 action ledger
10. FIX-10 tool drain
11. FIX-11 promote approval
12. FIX-12 live shard
```

## 统一验收指标

修复后重跑同一测试集，至少记录：

```text
time_to_first_accepted_flag
accepted_flags_per_hour
accepted_score_per_hour
no_finish_work_count
average_work_duration
repeated_target_attempts
partial_results_preserved
goal_met_without_acceptance_count
```

对比基线：

```text
v0.3:
  6h, 65 accepted, 19900 award

v0.4_new:
  6.8h, 37 accepted, 12430 award
```

目标不是单纯“跑得更久”，而是：

```text
单位时间有效 flag 更多
单个难题不再拖垮整个任务
未达标时不会被误判为 completed
```

## v0.5 跑分新增问题（Task #10）

- [x] Flag 归属改为 Intent.TargetKey（`challenge:a-08` → `a-08`），不再把 `cve-2025-*`、`index-0`、`on-disk` 当题目
- [x] 过滤描述、截断、示例和带空格的 flag 文本
- [x] 已有错误 challenge 标签可被 SyncTaskFlags 修正/删除
- [x] 同一 task 内 start/close 容器守卫：已 active 不重复 start；未 completed 不允许 close
- [x] f2-08 这类“先关环境、后离线提交”路径被 early-close guard 阻止
- [ ] 容器守卫仍是 runtime 内存状态，进程重启会丢；需要 container_leases 持久化
- [ ] 历史 Task #10 的重复 start/close 不会回滚；新任务生效

### [x] Submit 前容器/目标校验

- Runtime submit 必须带 `unique_code`
- 无 active 容器时拒绝 submit
- 已关闭容器拒绝 submit
- `budget.container_required=false` 仅用于明确 static/offline flag
- 已 completed 的 challenge 允许幂等校验

- [x] swarm/shard lane 完成时立即提交 flag；quorum 只合并信息

- [x] Merger 对合并后的 finding 再次提交新 flag，已 accepted 自动跳过

- [x] Task-scoped assets via asset_tasks; task page no longer lists global assets

- [x] Cross-task isolation: Worker sandbox and task-scoped chat assets

- [x] Cross-task evidence defense at runtime and ingestion

- [x] Solved target queues an immediate fast close Intent to release the container

- [x] Direct platform close after solved target; fast close Intent fallback

- [x] expected_flags fallback from facts; accepted flags tracked by hash

- [x] Close judgment moved to Planner (`close_target`); Engine only validates/executes

- [x] Parse quoted flag_count/is_completed facts; Planner can close platform-completed targets

- [x] Challenge completion lock: no reopen, no further flag submission

- [x] Record completion at submit time; prevent duplicate c-03/a-10 submissions

- [x] Previous failed attempts passed to the next Worker

- [x] Reconcile active Work counts and periodically dispatch queued Intents

- [x] Periodic platform completion reconciliation for missed submit callbacks

- [x] Idle worker capacity is reported to Planner after 5 minutes

- [x] Max-active start 409 no longer marks the challenge active

- [x] Ambiguous start responses judged by model; no hardcoded 409 message matching

- [x] Add report export agent restricted to export_report

- [x] Task detail 报告 tab

- [x] Report tab: three columns, 8 per page

- [x] Traffic history grouped by task/conversation with paginated detail

- [x] Conversation-scoped traffic schema/migration
