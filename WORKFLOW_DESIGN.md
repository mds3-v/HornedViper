# RNA / HornedViper 工作流设计 v0.3

> v0.3 已按你的拍板更新：
> - 层级采用 `Task → Goal → Intent → Work`
> - mainagent 只设定总目标；**planner 直接根据总目标生成 Intent**
> - 只审批 **Intent**，不审批 tool
> - `ctf` 模式全部自动通过；`pentest` 模式低风险自动、危险人工
> - `pentest` 的 `dangerous_actions` 只保留 `delete / modify / credential_access`
> - `ctf` 模式在用户提供提交途径时，由 `SubmitService` 自动提交 flag
> - planner 纯事件驱动：**worker 提供新的可用信息就激活 planner**
> - planner 采用**有界批量 + 高信号直通**，不等待“大部分 worker 完成”
> - 批量最大等待：CTF **2 分钟**，pentest **5 分钟**（持续事件流的上限；单个普通事件仍按 debounce 唤醒）
> - v1 直接做 UI，参考本地 ARTEX；两版都用 SQLite
> - v1 登录只做单用户
> - worker 只输出资产、事实、发现、证据，不给建议、不做规划
> - 两个项目都不只用于 CTF，CTF 只是可选模式
> - 日志不脱敏，直接存储

> 两个实现：
> - **HornedViper**：Go + Norma（`github.com/Autumn-27/norma`）
> - **RNA**：TypeScript + DeepSeek Harness（当前 DSH）
>
> 参考：ARTEX（`/Users/mds3/tools/ai/ARTEX`，AGPL-3.0）与文章《拆解 AI 渗透工具 ARTEX 架构》。

---

## 0. 已拍板

| 决策 | 结论 |
|---|---|
| 层级 | `Task → Goal → Intent → Work` |
| 目标拆解 | mainagent 只设定总 Goal；planner 直接从 Goal 生成 Intent |
| 唯一意图生成者 | planner |
| 审批对象 | 只审批 Intent，不审批 tool |
| CTF 模式 | 所有 Intent 自动通过，无人工审批 |
| 渗透模式 | 低风险自动通过；危险 Intent 人工审批 |
| 危险动作 | 仅 `delete` / `modify` / `credential_access` |
| CTF flag | 用户提供提交途径时，`SubmitService` 自动提交 |
| planner 唤醒 | 纯事件驱动；worker 有新可用信息就激活 |
| 批量最大等待 | CTF **2min**；pentest **5min** |
| 存储 | HornedViper 与 RNA 都用 SQLite |
| UI | v1 直接做 UI，信息架构与组件参考 ARTEX；单用户登录 |
| worker 输出 | 仅资产、事实、发现、证据；无建议、无规划 |
| 日志 | 不脱敏，原文存储 |
| 适用范围 | 通用任务；CTF 是可选模式，渗透是另一种模式 |

---

## 1. 术语

| 层级 | 名称 | 谁产生 | 含义 |
|---|---|---|---|
| L0 | **Task** | 用户 / mainagent | 一次完整任务：比赛、渗透、研究 |
| L1 | **Goal** | mainagent（LLM） | 总目标，可验收的最终结果 |
| L2 | **Intent** | **planner（唯一）** | 下一步做什么；planner 直接根据 Goal 生成 |
| L3 | **Work** | 调度器 / worker | 一次执行；一个 Intent 可 1..N 个 Work |
| L4 | **Attempt** | 调度器 | 同一 Work 的重试 |

补充：

- **Goal 不做多层拆解**。mainagent 可以设置一个或多个顶层 Goal（用户明确给出多个时），但不把 Goal 拆成子 Goal；拆解工作由 planner 直接用 Intent 表达。
- 一个 Intent 可以有多个 Work：`single`（1 个）、`shard`（按资产分片）、`swarm`（多策略并行）。
- 不再使用“任务”指代 Intent，避免歧义。

---

## 2. 设计原则

1. **LLM 决策，工作流约束**：LLM 决定目标、意图、工具、策略；工作流只做 schema、状态、权限、预算、日志。
2. **唯一意图生成者**：只有 planner 有 `propose_intents`；其他角色没有。
3. **单写者**：资产图、探索图、证据写入都走受控服务；Agent 只提交原始信息。
4. **审批只在 Intent 层**：不做 tool 级审批；`ctf` 全放行，`pentest` 低风险自动、危险人工。
5. **事件驱动**：图变更、work 完成、hint/goal 变更触发 planner；没有新方向返回 0 条，系统休眠。
6. **每次唤醒新会话**：planner/worker 都是新 session；跨会话靠图、todo、日志。
7. **worker 只产出事实**：资产、事实、发现、证据；不写建议、不写下一步、不做规划。
8. **日志是证据**：请求/响应/思考/工具调用全部落库，能按图节点反查。
9. **先 single，再 shard，最后 swarm**。

---

## 3. 总体架构

```text
                         Human
                           │ 总目标 / 提示 / 审批 / 暂停
                           ▼
                      ┌───────────┐
                      │ mainagent │ 设定 Goal + 验收 + 问答
                      └─────┬─────┘
                            │ Goal / Hint / Constraint
                            ▼
┌───────────────────────────────────────────────────────────┐
│                Workflow Kernel（确定性）                   │
│                                                           │
│  Event Log · Scheduler/Lease · Approval · Budget · Trace  │
│                                                           │
│  ┌─────────────────────────────────────────────────────┐  │
│  │        Graph Service（探索图 / 资产图 / Anchor）      │  │
│  └─────────────────────────────────────────────────────┘  │
└───────────────┬───────────────────────────┬───────────────┘
                │ wake packet               │ intent / work
                ▼                           ▼
         ┌───────────┐              ┌──────────────┐
         │  planner  │── intents ──►│  Approval    │
         │ 唯一意图源 │              │  Intent 级    │
         └─────┬─────┘              └──────┬───────┘
               │                           │ approved
               │                           ▼
               │                    ┌──────────────┐
               │                    │ worker pool  │
               │                    │ 执行 work     │
               │                    └──────┬───────┘
               │                           │ 回写
               └──── assets/facts ◄────────┘
                     findings/evidence
```

外部依赖：

- **SQLite**：图、事件、审批、日志、预算。WAL 模式，单进程多协程。
- **文件系统**：附件、截图、脚本、证据、导出报告。
- **LLM API**：外部模型。
- **工具执行环境**：容器/沙箱（可选，但推荐）。

---

## 4. 角色契约

### 4.1 mainagent（总调度者 / 人类接口）

**只做**：

- 设定总 Goal（一个或多个顶层目标）+ 验收标准；
- 维护 Hint、Constraint；
- 验收 planner 的“目标达成”提议；
- 回答人类问题、展示进展；
- 暂停/恢复/终止。

**不做**：

- 不拆解 Goal；
- 不生成 Intent；
- 不执行工具；
- 不审批自己的目标。

**工具**：

| 工具 | 作用 |
|---|---|
| `set_goal(text, acceptance_criteria, constraints[])` | 设定总目标 |
| `add_hint(text, asset_ids?)` | 战略提示 |
| `add_constraint(type, text)` | allow/deny 约束 |
| `accept_goal(goal_id, evidence_ids, reason)` | 验收目标 |
| `reject_goal(goal_id, reason)` | 驳回达成提议 |
| `get_overview()` | 任务总览 |
| `get_trace(node_id)` | 证据链 |
| `pause/resume/cancel()` | 任务控制 |

---

### 4.2 planner（唯一意图生成者）

**只做**：

- 读 wake packet：Goal、增量图、Hint、Constraint、Todo、运行中的 Work、预算；
- **直接根据总 Goal 生成 0..N 条 Intent**；
- 维护跨唤醒 Todo；
- 对运行中的 Work 做 `steer` / `kill`（可选）；
- 提议某个 Goal 达成（附证据），但不自行标记。

**不做**：

- 不执行工具；
- 不写 Fact/Finding/Asset；
- 不审批；
- 不拆分层级 Goal。

**工具**：

| 工具 | 作用 |
|---|---|
| `propose_intents(intents[])` | 0..N 条 Intent；空数组合法 |
| `update_todo(items[])` | 持久化多步计划 |
| `get_node` / `list_facts` / `list_findings` / `list_assets` | 按需补读 |
| `steer_work(work_id, message)` | 给运行中的 worker 纠偏 |
| `kill_work(work_id, reason)` | 终止跑偏的 work |
| `propose_goal_met(goal_id, evidence_ids, reason)` | 提交验收提议 |

**Intent 契约**：

```json
{
  "summary": "一句话：做什么 + 为什么",
  "rationale": "基于哪些 Goal / Fact / Finding",
  "goal_ids": [1],
  "parent_fact_ids": [12, 15],
  "asset_ids": [101, 102],
  "priority": 7,
  "risk": "low | medium | high | critical",
  "dangerous_actions": ["delete", "modify", "credential_access"],
  "execution_mode": "single | shard | swarm",
  "fanout": { "by": "asset", "max_workers": 3 },
  "swarm": {
    "lanes": [
      { "name": "lane-a", "strategy": "..." },
      { "name": "lane-b", "strategy": "..." }
    ],
    "quorum": 2
  },
  "acceptance_criteria": ["结果可复现"],
  "budget": {
    "max_minutes": 20,
    "max_tokens": 200000,
    "max_turns": 40
  },
  "required_tools": ["bash", "read", "write"],
  "expected_outputs": ["fact", "finding", "asset", "evidence"]
}
```

**约束**：

- 没有新方向 → `[]`；
- 不为“看起来在工作”重复派已有 Intent；
- 有依赖的步骤等前置 Fact 写入后再派；
- 每次唤醒读 Todo，避免重复/漏步；
- `risk` 和 `dangerous_actions` 由 planner 判断，审批服务据此路由。

---

### 4.3 worker（意图执行者）

**只做**：

- 领取一个 Work；
- 用工具完成任务；
- 回写 **资产、事实、发现、证据**；
- 用 `finish(status)` 结束。

**不做**：

- 不生成 Intent；
- 不给建议；
- 不写下一步/规划；
- 不修改 Goal；
- 不审批；
- 不直接写数据库；
- 不扩大范围。

**工具**：

| 工具 | 作用 |
|---|---|
| `record_assets(assets[])` | 提交原始资产，由程序规范化 |
| `record_facts(facts[])` | 写 Fact + evidence + confidence |
| `report_findings(findings[])` | 写 Finding + severity + evidence |
| `attach_evidence(refs[])` | 脚本/截图/流量/输出文件 |
| `finish(status, error?)` | 结束；status = done/blocked/failed |
| 领域工具 | bash/read/write/web/MCP/... |

**Work 输出（只有这四类）**：

```json
{
  "assets": [
    { "type": "endpoint", "value": "https://target/api/login", "metadata": {} }
  ],
  "facts": [
    {
      "summary": "一句话结论",
      "detail": "细节",
      "evidence": "一行关键证据：命令 + 输出",
      "confidence": "observed | inferred",
      "asset_ids": [101]
    }
  ],
  "findings": [
    {
      "title": "发现标题",
      "severity": "info | low | medium | high | critical",
      "evidence": "复现步骤/证据",
      "asset_ids": [101]
    }
  ],
  "evidence": [
    { "type": "file", "path": "artifacts/run-21/solve.py" }
  ]
}
```

明确：

- **没有 `summary`、没有 `next_suggestions`、没有 `recommendations`、没有 `plan`**。
- `finish(status, error?)` 只表示执行状态，不是规划。
- UI 如果需要一句话摘要，由 runtime 从 facts 自动生成，不是模型输出。
- 负结论必须带 evidence，标 `observed/inferred`，不能凭空“未发现”。

---

### 4.4 approver（Intent 审批）

**只做**：

- 审批 Intent；
- 根据任务模式执行策略：
  - `ctf`：全部自动通过；
  - `pentest`：低风险自动，危险人工；
- 记录审批结果、理由、审批人/策略。

**不做**：

- 不审批 tool；
- 不执行工具；
- 不生成 Intent；
- 不修改图。

**实现形态**：

- `ApprovalService`（确定性策略）+ 人工审批台；
- planner 在 Intent 中给出 `risk` / `dangerous_actions`；
- 策略服务只做路由，不替 planner 思考；
- 需要语义复核时可选用 LLM reviewer，但它只给风险建议，不执行、不审批 tool。

**工具**：

| 工具 | 作用 |
|---|---|
| `approve_intent(intent_id, reason)` | 人工/策略通过 |
| `reject_intent(intent_id, reason)` | 人工/策略拒绝 |
| `list_pending_intents()` | 审批列表 |

---

### 4.5 swarm_lead（可选）

- 只接收一个 `execution_mode=swarm` 的 Intent；
- 启动 2–5 个 lane worker（同一个 Intent）；
- 维护共享黑板；
- 合并、去重、quorum；
- 不生成新 Intent，不扩范围，不绕过审批。

---

### 4.6 supervisor（确定性看门狗）

不是 LLM：

- 租约过期、work 僵死；
- 预算/时间/token 超限；
- 平台 429/5xx；
- 生成 `work.timed_out` / `lease.expired` 事件；
- **不设定时 planner 心跳**，只发事件。

---

## 5. 主循环（纯事件驱动）

### 5.1 事件

| 事件 | 触发 |
|---|---|
| `task.started` | 任务创建 |
| `goal.set` | mainagent 设定/修改 Goal |
| `hint.added` | 人工/主 Agent 加提示 |
| `constraint.added` | 加约束 |
| `intent.proposed` | planner 产出 |
| `intent.approved` / `intent.rejected` | 审批 |
| `work.claimed` | worker 领取 |
| `work.finished` | worker 完成 |
| `work.failed` | worker 失败 |
| `work.timed_out` | 看门狗 |
| `fact.recorded` | 回写事实 |
| `finding.reported` | 回写发现 |
| `asset.recorded` | 回写资产 |
| `goal.met_proposed` | planner 提议达成 |
| `goal.accepted` / `goal.rejected` | 验收 |
| `task.resumed` | 恢复/重启 |

### 5.2 循环

```text
事件发生
  → 写入 workflow_events
  → 防抖/合并（例如 2s）
  → 如果没有 planner 在跑：启动一次 planner
  → planner 读 wake packet
  → propose_intents(0..N)
  → 校验 → 审批 → 入队
  → worker claim → 执行 → 回写 → work.finished
  → 再次触发事件
```

要点：

- **没有周期性 planner 唤醒**；没有事件就休眠。
- planner 运行期间到达的事件标记 dirty；本轮结束后再唤醒一次。
- 同一 Task 同时只有一个 planner。
- `work.timed_out` / `lease.expired` 也是事件，不是轮询。
- 启动/重启时合成 `task.resumed` 事件。

### 5.3 没有新方向

```text
planner 返回 []
  → 记录 planner_no_intent
  → 不立刻再唤醒
  → 等下一个事件（新 fact/finding/hint/goal/work）
```

### 5.4 Planner 激活策略：有界批量 + 高信号直通

你的问题：**要不要等大部分 worker 完成再激活 planner，以省 token？会不会拖慢难题？**

结论：

- **不要等“大部分 worker 完成”**。等待会造成 head-of-line blocking：N-1 个 worker 完成、1 个慢，planner 不跑，已完成的结果无法变成新 Intent，其他 worker 空闲。
- **要批量，但批量的是“事件”，不是“worker 完成数”**。把短时间内的多个 fact/asset 合并成一次 wake，既省 token，又不阻塞。
- **高信号事件直通**：finding/flag、blocked/failed/timed_out、hint/goal/constraint、审批结果，立即唤醒（0–2s），不等批量。
- **普通事件批量**：新 fact/asset/evidence，debounce 2–5s；达到阈值或最大等待就唤醒。单个普通事件也会在 debounce 后唤醒，`maxDelay` 只是持续事件流的上限。
- **所有 worker 都结束**：立即唤醒，不要等。
- **难题**：不要等慢 worker；它产出可用信息就唤醒，不产出就让它继续跑，超时事件再唤醒。planner 可以用已完成的部分先规划。

这里的“可用信息”指：

| 信息 | 信号强度 | 处理 |
|---|---|---|
| 新 finding / flag | 高 | 立即唤醒 |
| work blocked / failed / timed_out | 高 | 立即唤醒 |
| 新 hint / goal / constraint / 审批结果 | 高 | 立即唤醒 |
| 新 fact | 中 | 批量，debounce |
| 新 asset | 中低 | 批量，可合并 |
| 新 evidence（脚本/截图） | 低 | 批量，通常随 fact/finding 一起 |
| work 心跳/进度 | 无 | 不唤醒，除非产生上述信息 |

伪代码：

```text
onEvent(e):
  if e is high-signal:
    requestWake(e, delay=0)
  else:
    pending.add(e)
    requestWake(e, delay=debounceMs)

  if activeWorkers == 0:
    requestWake(e, delay=0)
  else if pending.size >= batchCount
       or pending.oldestAge >= maxDelayMs
       or finishedSinceLastWake >= max(1, ceil(activeWorkers * batchFraction)):
    requestWake(e, delay=0)

requestWake(e, delay):
  if plannerRunning:
    dirty = true
    return
  scheduleAfter(delay, wakePlanner)

wakePlanner:
  plannerRunning = true
  packet = buildPacket(pending, dirty, recentEvents)
  runPlanner(packet)
  plannerRunning = false
  pending.clear()
  if dirty:
    dirty = false
    requestWake("dirty", delay=minIntervalMs)
```

配置建议：

```json
{
  "plannerWake": {
    "mode": "batched",
    "debounceMs": 3000,
    "maxDelayMs": 120000,
    "batchCount": 3,
    "batchFraction": 0.5,
    "highSignalBypass": true,
    "minIntervalMs": 5000,
    "maxPacketFacts": 20
  }
}
```

按模式调：

| 模式 | debounce | maxDelay | 说明 |
|---|---|---|---|
| ctf | 2s | **2min** | 题目短、节奏快；上限只防持续事件流 |
| pentest | 5s | **5min** | 任务长、事件稀疏；更偏向省 token |

> `maxDelay` 不是常规延迟。单个普通事件（例如一条新 fact）会在 `debounce` 后唤醒 planner；只有事件持续不断、debounce 被反复重置时，才会等到 `maxDelay` 的硬上限。高信号事件仍然 0–2s 直通。

成本模型：

```text
C_wake = C_system + C_packet + C_reads + C_output
批量 K 个事件：1 次 wake vs K 次 wake
节省 ≈ (K - 1) × C_wake - Δpacket
延迟 ≤ maxDelay
```

但要注意：

- packet 不能无限大：只放 top-K 摘要 + 计数，细节让 planner 用工具查；
- 等待“大部分完成”省下的 token，可能被 worker 空闲时间、整体 makespan 抵消；
- planner 频繁唤醒还会产生重复/重叠 Intent，批量反而减少重复；
- 如果 planner 一次 wake 的成本很高，优先优化 system prompt / packet，而不是无限等待。

指标：

- `planner_wakes`、`tokens_per_wake`、`events_per_wake`
- `time_to_plan_p95`（事件到 planner 开始）
- `worker_idle_time`、`task_makespan`
- `duplicate_intent_rate`
- `planner_no_intent_rate`

建议先做 A/B：`immediate` vs `debounce` vs `batch`，用同一批回放任务比较“总 token / 总时长 / 重复 Intent”。

---

## 6. 状态机

### Task

```text
created → planning → running → settling → completed
              │          │          │
              │          ├→ paused ─┘
              │          ├→ cancelled
              │          └→ failed
              └→ cancelled
```

### Goal

```text
open → in_progress → met
  │         │
  │         └→ abandoned
  └→ abandoned
```

### Intent

```text
proposed → approved → queued → claimed → running → succeeded
    │          │                    │         ├→ failed
    │          └→ rejected          │         ├→ blocked
    │                               │         └→ cancelled
    └→ withdrawn                    └→ expired
```

### Work

```text
leased → running → settling → succeeded
                    │         ├→ failed
                    │         ├→ timed_out
                    │         └→ cancelled
                    └→ lost（租约过期）
```

### Approval

```text
pending → approved
        ├→ rejected
        ├→ expired（默认拒绝）
        └→ escalated（人工）
```

### 守卫

- 只有 `approved` 的 Intent 能入队；
- 只有 `queued` 的 Intent 能被 claim；
- 同一 Work 只有一个 lease；
- `succeeded` 的 Intent 不再派新 Work；
- Goal `met` 必须有人类/mainagent 的验收记录；
- 所有状态变更写 `workflow_events`。

---

## 7. 数据模型（SQLite）

### 7.1 表清单

| 表 | 作用 |
|---|---|
| `tasks` | 任务 |
| `goals` | 目标 |
| `intents` | 意图 |
| `works` | 执行 |
| `exploration_nodes` | 探索图节点 |
| `exploration_edges` | 探索图边 |
| `assets` | 资产图节点（全局） |
| `asset_edges` | 资产图边 |
| `exploration_anchors` | 探索节点 ↔ 资产 |
| `activities` | 逐步过程 |
| `llm_calls` | 请求/响应/思考/usage |
| `approvals` | Intent 审批 |
| `workflow_events` | 状态变更事件 |
| `artifacts` | 证据文件 |
| `task_constraints` | allow/deny |
| `task_scope` | 资产范围 |
| `planner_todos` | planner 跨唤醒待办 |
| `swarm_lanes` | 蜂群 lane |

### 7.2 关键字段

**tasks**

```
id, title, mode ('ctf'|'pentest'|...), goal_id,
status, created_at, started_at, ended_at,
budget_json, settings_json
```

**goals**

```
id, task_id, text, acceptance_criteria,
status, created_by, created_at,
met_at, met_by, evidence_json
```

**intents**

```
id, task_id, goal_ids[], summary, rationale,
parent_fact_ids[], asset_ids[], priority, risk,
dangerous_actions[], execution_mode,
fanout_json, swarm_json, budget_json,
required_tools[], expected_outputs[],
status, created_by_llm_call_id,
approval_id, created_at
```

**works**

```
id, task_id, intent_id, worker_id, lane_id,
status, lease_expires_at, started_at, ended_at,
stop_reason, token_usage, cost
```

**exploration_nodes**

```
id, task_id, kind, payload_json, state,
priority, origin, created_by_role,
created_by_work_id, created_by_llm_call_id,
created_at, updated_at
```

**exploration_edges**

```
task_id, src_id, dst_id, rel, created_at
```

**assets**

```
id, type, canonical_key, payload_json,
parent_id, scope_status, source,
first_seen, last_seen
```

**activities**

```
id, task_id, work_id, intent_id, role,
kind, tool, tool_use_id, summary, detail,
metadata_json, llm_call_id, created_at
```

**llm_calls**

```
id, task_id, work_id, role, session_id, turn,
provider, model, request_json, response_json,
thinking, tool_calls_json, usage_json,
latency_ms, error, request_hash, created_at
```

**approvals**

```
id, task_id, intent_id, risk, decision,
decided_by ('policy:ctf'|'policy:auto'|'human'),
reason, created_at, decided_at
```

**workflow_events**

```
id, task_id, type, actor_type, actor_id,
subject_type, subject_id, payload_json,
correlation_id, causation_id, created_at
```

### 7.3 SQLite 配置

```sql
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;
PRAGMA synchronous = NORMAL;
PRAGMA foreign_keys = ON;
```

并发规则：

- **单进程**；读多连接，写单连接或写队列。
- 领取 Intent 用 `BEGIN IMMEDIATE` + `UPDATE ... RETURNING`，不用 `SKIP LOCKED`。
- 图写入串行化（mutex/queue），事务尽量短。
- 定期 `PRAGMA wal_checkpoint(TRUNCATE)`；备份用 `VACUUM INTO`。
- 多进程不是 v1 目标；以后需要再迁 Postgres。

### 7.4 索引

- `assets(type, canonical_key)` 唯一；
- `exploration_edges(task_id, src_id, rel, dst_id)` 唯一；
- `intents(task_id, status, priority DESC)`；
- `works(task_id, status, lease_expires_at)`；
- `activities(task_id, id)` / `activities(work_id, id)`；
- `llm_calls(task_id, id)` / `llm_calls(work_id)`；
- `approvals(task_id, decision)`；
- `exploration_anchors(asset_id)`。

---

## 8. 两张图

### 8.1 探索图（每任务一份，隔离）

节点：

| kind | 状态 |
|---|---|
| `goal` | open / in_progress / met / abandoned |
| `intent` | proposed / approved / queued / claimed / running / succeeded / failed / blocked / cancelled |
| `fact` | confirmed / dismissed |
| `finding` | confirmed / dismissed |
| `hint` | active / consumed |
| `artifact` | active |

边：

| rel | 语义 |
|---|---|
| `spawns` | Goal → Intent；Task → Goal |
| `yields` | Intent → Fact |
| `derived_from` | Intent → 上游 Fact/Finding；Fact → Fact |
| `proves` | Fact/Finding → Goal |
| `anchors` | 探索节点 → 资产 |
| `supersedes` | 新结论取代旧结论 |
| `contradicts` | 冲突结论 |

写入规则：

- Agent 只提交原始信息；
- 程序校验 `intent_id` 属于本 task、evidence 非空、asset 存在；
- 去重 `(task_id, intent_id, summary_hash)`；
- 每条 fact/finding 记录 `created_by_work_id` / `created_by_llm_call_id`；
- 负结论必须带 evidence/confidence。

### 8.2 资产图（全局唯一）

```text
root_domain → subdomain → ip → service → app → endpoint
```

- Agent 只提交 `{type, value, metadata}`；
- 程序规范化 `canonical_key`、去重、父子关系、范围；
- `scope_status`：in_scope / out_of_scope / unknown；
- `source`：agent / import / manual。

### 8.3 Anchor

`exploration_anchors(node_id, asset_id)`：

- Intent 锚定目标资产；
- Fact/Finding 锚定涉及资产；
- 双向查询：Intent → 资产；资产 → Intent/Fact/Finding。

---

## 9. 审批（只审批 Intent）

### 9.1 两种模式

| 模式 | 审批 |
|---|---|
| `ctf` | 所有 Intent 自动通过；`decided_by=policy:ctf`；无人工 |
| `pentest` | 低风险自动；危险 Intent 人工 |

### 9.2 风险与路由

| risk | dangerous_actions | pentest 路由 |
|---|---|---|
| low | 无 | 自动通过 |
| medium | 无 | 自动通过 |
| high | 有/无 | 人工 |
| critical | 有 | 人工 |
| 任意 | 含 delete / modify / credential_access | 人工 |
| 任意 | 越界（scope 检查） | 拒绝 |

`dangerous_actions` 只保留三个：

```
delete | modify | credential_access
```

说明：

- `privilege_escalation`、`lateral_movement` 不单独列为 dangerous_action，由 `risk=high/critical` 走人工；
- `out_of_scope` 不是审批，是 scope 检查，直接拒绝；
- 默认 low/medium 自动，high/critical 人工；含上述三个动作时无论 risk 都人工。

### 9.3 审批记录

```json
{
  "intent_id": 7,
  "mode": "pentest",
  "risk": "high",
  "dangerous_actions": ["modify"],
  "decision": "pending_human",
  "decided_by": null,
  "reason": "修改数据，需人工确认",
  "created_at": "..."
}
```

人工通过后：

```json
{
  "decision": "approved",
  "decided_by": "human:alice",
  "reason": "仅允许修改测试库",
  "decided_at": "..."
}
```

### 9.4 不做 tool 审批

- 不加 `tool_call` 审批流；
- 工具层不做“逐次批准”；
- 如果确实需要硬边界，可在执行环境做 **scope/workdir/deny 拦截**（这是执行约束，不是审批）；
- 所有 Intent 的审批记录是唯一审批凭证。

### 9.5 审批与执行分离

- worker 不能审批自己的 Intent；
- 审批者不执行工具；
- 同一 Intent 只有一个最终决定；
- 审批记录不可修改，只能追加人工覆盖。

---

## 10. 日志与可溯源（不脱敏）

### 10.1 三层日志

| 层 | 表 | 内容 |
|---|---|---|
| LLM | `llm_calls` | 请求/响应、system prompt、messages、thinking、tool_calls、usage、延迟、错误 |
| 执行 | `activities` | thinking、tool_use、tool_result、decision |
| 领域 | `workflow_events` / `approvals` / 图节点 | 状态、审批、事实/发现/资产来源 |

### 10.2 追溯链

```text
Finding #42
  → activity #900
    → llm_call #1200
      → request_json / response_json
  → evidence artifact #7
  → proves Goal #1
  → anchors Asset #101
```

### 10.3 存储策略

- **不脱敏，原文存储**；包括 headers、cookies、token、请求体、响应体。
- 大响应（HTML/二进制）放文件/对象存储，`llm_calls` 存摘要 + 路径 + hash。
- 默认全量保留；提供手动归档/清理。
- 数据库文件和日志目录的访问权限要收紧（这是访问控制，不是脱敏）。
- 支持 `trace_level: full | summary | off` 按任务配置。

### 10.4 捕获点

**Norma**：

- `transcript.Store`：原始对话；
- 包装 `llm.Provider`：request/response/usage；
- `harness` 事件：thinking/tool_use/tool_result；
- `subagent` transcript sidechain：lane 独立追踪。

**DSH**：

- `llm/stream` waterfall：请求/响应；
- `session/event` / `sessionProjections`：消息、tool call、turn；
- `tools/result`：工具结果；
- 插件把 `session_id` / `event_seq` / `llm_call_id` 写进图节点。

---

## 11. 蜂群

### 11.1 触发

- planner 在 Intent 中设置 `execution_mode=swarm`；
- 或同一 Intent 失败 N 次后，planner 下一轮升级为 swarm；
- 需要预算和 lane 上限。

### 11.2 结构

```text
Intent #7 (swarm)
  ├── lane A ─┐
  ├── lane B ─┼─→ Shared Blackboard → swarm_lead merge → Work Result
  └── lane C ─┘
```

### 11.3 规则

- lane 只执行同一个 Intent，不生成新 Intent；
- lane 共享 `intent_id` 作用域的黑板；
- finding 需要 K 个独立 lane 确认，或确定性复现；
- lead 合并、去重、写回；
- 一个 lane 成功后中断其他 lane；
- lane 数建议 3–5；
- 总预算在 lanes 间分配；
- lane 失败不阻塞其他 lane。

### 11.4 输出

lead 最终只写四类：资产、事实、发现、证据。没有建议、没有规划。

---

## 12. 模式

### 12.1 `ctf` 模式

- 审批：全部自动通过；
- Goal：比赛目标（例如“拿到尽可能多的 flag”）；
- Intent：解题、分析、利用、提交；
- 资产：题目、附件、实例、远程目标；
- 事实：分析结论；
- 发现：flag、漏洞；
- 证据：脚本、输出、截图；
- `SubmitService`：当 finding 类型为 `flag` 且证据充分时自动提交；**前提是用户已提供提交途径**（平台 API/token、命令行、适配器配置）。未提供时只记录，不提交；
- 提交前做格式校验、去重、限流；提交结果写回 `findings` / `workflow_events`；accepted 后停止同题其他 Work；
- 无人工审批；保留人工 kill switch。

### 12.2 `pentest` 模式

- 审批：low/medium 自动，high/critical/危险人工；
- Goal：授权的测试目标；
- Intent：侦察、验证、利用；
- 资产：域名/IP/服务/应用/端点；
- 事实：指纹、配置、行为；
- 发现：漏洞；
- 证据：请求/响应、脚本、截图；
- 严格 scope/constraint。

### 12.3 其他模式

预留 `mode` 字段；新增模式只改审批策略 + 领域工具，不改内核。

---

## 13. UI（参考 ARTEX）

### 13.1 信息架构

| ARTEX 页面 | 新页面 | 内容 |
|---|---|---|
| Dashboard | 总览 | 任务数、Intent/Work 统计、token、活动流 |
| Tasks | 任务 | 任务列表 + 详情 |
| Findings | 发现 | 全局发现、证据、复现 |
| Assets | 资产 | 资产图、覆盖度、范围 |
| Traffic | 证据/流量 | 请求响应、截图、脚本 |
| LLM Records | LLM 调用 | 请求/响应/思考/usage |
| Workspace | 工作区 | 文件、脚本、报告 |
| System/Agents | 角色配置 | main/planner/worker/approver 提示词与工具 |
| System/Intercept | 审批 | 只列 Intent 审批 |
| System/LLM | 模型 | provider/model/参数 |
| System/Logs | 日志 | 运行日志 |
| System/MCP/Skills/Tools | 工具 | MCP/技能/工具管理 |
| Settings | 设置 | 模式、预算、保留策略 |

任务详情标签页：

```text
Overview | Plan (Goal/Intent/Work) | Sessions | Graph |
Findings | Assets | Evidence | Approvals | Trace | Chat
```

- **Plan**：Goal 列表、Intent 队列、Work 泳道；
- **Sessions**：planner/worker 的思考与工具调用流；
- **Graph**：探索图（force layout，可折叠）；
- **Trace**：点 Fact/Finding → activity → llm_call；
- **Approvals**：Intent 审批卡片，支持通过/拒绝/理由；
- **Chat**：mainagent 对话，支持加 hint/改 goal/暂停。

### 13.2 CTF 模式额外页面

- Challenges：题目列表、分类、分值、状态；
- Scoreboard：分数、排名、时间；
- Submissions：提交记录、flag、结果；
- 这些是 `ctf` 领域的页面，不影响内核。

### 13.3 组件复用

参考 ARTEX 的：

- `exploration-graph`：力导向探索图；
- `transcript`：会话/工具流；
- `approval-records`：审批卡片；
- `status-badge` / `table-pagination` / `todo-popover` / `markdown`；
- 侧边栏 + 主题 + 布局。

### 13.4 RNA 的 UI 形态

- HornedViper：Next.js 应用（类似 ARTEX）。
- RNA：DSH 的 React slot 页面；**不要**用 iframe 嵌一个独立 Web 应用，否则拿不到主题/locale。
- 两端共享页面信息架构和组件语义；视觉可各自适配。

### 13.5 登录与权限（单用户）

- v1 **单用户**：一个管理员账号，登录后拥有全部权限。
- 不做多用户、角色、租户、团队空间。
- 首次启动保留 setup 设置密码页（参考 ARTEX 的 `/setup`）。
- 会话使用 cookie/JWT；本地部署默认只监听 `127.0.0.1`。
- RNA 复用 DSH 的会话身份，不另做账号体系；插件页面按单用户处理。
- 后续如需多用户，再加 `users/roles/task_owner` 表，不影响内核。

---

## 14. 两个底座

### 14.1 共享契约

```text
contracts/
  intent.schema.json
  work-result.schema.json
  approval.schema.json
  event.schema.json
  graph.schema.json
  prompts/
    mainagent.md
    planner.md
    worker.md
    approver.md
    swarm-lead.md
  schema.sql
```

两版实现同一套状态机、表结构、工具名、事件名、提示词边界。

### 14.2 Norma / HornedViper（Go + SQLite）

| 关注点 | Norma |
|---|---|
| Agent 循环 | `agentcore.Session` |
| 角色提示 | `Options.SystemPrompt` |
| 工具 | `tool.CoreTool` |
| 权限 | `permission`（只做硬边界，不做审批） |
| 原始对话 | `transcript.Store` |
| 压缩 | `compaction.Config` |
| Hook | `harness.HookRunner` |
| 子 Agent | `subagent` / `coordinator.RunParallel` |
| 待办 | `tool.TodoStore` |
| 存储 | `modernc.org/sqlite` 或 `mattn/go-sqlite3` |
| UI | Next.js（参考 ARTEX） |

### 14.3 DSH / RNA（TypeScript + SQLite）

| 关注点 | DSH |
|---|---|
| Agent 会话 | `ctx.agents` / `ctx.subagents.startActivation` |
| 角色提示 | Agent preset |
| 工具 | `ctx.tools.register` |
| 权限 | `tools/pre-execute` / `guard`（只做硬边界） |
| 人工审批 | `ctx.approval.request`（只用于 Intent） |
| 会话日志 | `session/event` / `sessionProjections` |
| LLM 原文 | `llm/stream` |
| 工具结果 | `tools/result` |
| 子 Agent | `ctx.subagents` |
| 蜂群 | Agent Teams（实验性）或 plugin-managed lanes |
| 存储 | SQLite（插件管理） |
| UI | React slot 页面 |

### 14.4 许可证

- Norma：Apache-2.0，可放心用。
- ARTEX：AGPL-3.0；参考架构没问题，直接复制代码会触发 AGPL。

---

## 15. 实施路线

### Phase 0：契约 + 内核 + SQLite（约 1 周）

- `contracts/` + `schema.sql`；
- 状态机、事件、租约、预算；
- Fake LLM / Fake tools 跑通：
  `Goal → Intent → Work → 四类输出 → 事件 → 再规划`；
- CLI 验证，不做 UI。

### Phase 1：角色 + 单 worker + UI 骨架（1–2 周）

- mainagent：`set_goal` / 验收；
- planner：wake packet + 0..N Intent + todo；
- worker：四类输出 + finish；
- approval：ctf 全放行 / pentest 策略 + 人工；
- SQLite + REST/SSE；
- UI：总览、任务列表、任务详情、审批、会话/工具流（参考 ARTEX）。

### Phase 2：图 + 追溯 + 恢复（1 周）

- 探索图/资产图/Anchor；
- LLM Records / Trace viewer；
- 崩溃恢复、租约回收；
- 预算/限流/重试；
- 指标。

### Phase 3：并行 + 蜂群（1–2 周）

- `shard`；
- `swarm` + 共享黑板 + quorum；
- swarm_lead；
- 失败升级；
- benchmark。

### Phase 4：RNA / DSH 移植

- 复用 `contracts/`；
- DSH 插件 + presets + tools + approval + UI；
- 用同一批回放数据验证语义一致。

---

## 16. HornedViper 上下文管理

> 目标：让每个角色的上下文都**有界、可解释、可恢复**，同时尽量提高 KV cache 命中率、减少重复读取和压缩损失。

### 16.1 核心原则

1. **上下文不是记忆**。记忆在图（Fact/Finding/Asset）、Todo、artifact、日志里；会话只是工作台。
2. **每个角色独立上下文**。不要把 mainagent、planner、worker 塞进同一个 session。
3. **稳定前缀 + 动态后缀**。角色提示、工具 schema、静态策略放系统提示；wake packet、增量事实放 user message。
4. **摘要进上下文，细节进工具/文件**。planner 看摘要，需要时用 `get_node` / `list_facts` 补读。
5. **压缩后必须重新注入关键状态**：Goal、Constraint、当前 Intent、Todo。
6. **大输出落文件**。Bash/HTTP/HTML 的大结果写 artifact，上下文只留摘要 + 路径 + 行号。
7. **planner/worker 每次新会话**；mainagent 长会话但分段。

### 16.2 三层压缩

```text
第 1 层：工具输出压缩
  Bash/Read/Grep/HTTP 大结果 → 文件 + 摘要 + 指针

第 2 层：会话压缩
  Norma compaction：MicroCompact → AutoCompact → Reactive
  mainagent / worker / swarm lead 使用

第 3 层：图压缩（cold digest）
  老的 Fact / settled Intent → digest 节点
  planner 只看 hot 节点 + digest 摘要
```

### 16.3 角色预算

以 200k 窗口为例；其他窗口按比例调整。

| 角色 | 生命周期 | 系统提示 | 动态上下文 | 工具输出 | 输出预留 | 压缩 |
|---|---|---|---|---|---|---|
| mainagent | 长会话，分段 | 6–10k | 20–30k（Goal/Constraint/Todo/Top findings） | 20–40k | 8k | AutoCompact + 分段 |
| planner | 每次 wake 新会话 | 6–8k | 8–16k（wake packet） | 8–16k（补读） | 4k | 通常不需要；超过 60% 再压 |
| worker | 每次 work 新会话 | 4–6k | 4–8k（brief） | 30–60k | 8k | AutoCompact + spill |
| approver | 每次审批新会话 | 2–4k | 2–4k | 0 | 1k | 不需要 |
| swarm lead | 每个 Intent | 4–6k | 8–12k（lane 摘要） | 8–16k | 4k | AutoCompact |
| lane worker | 每次 lane 新会话 | 4–6k | 4–8k（lane brief） | 30–60k | 8k | 同 worker |

原则：

- 不允许任何一个角色的上下文超过窗口的 **80%**；
- 工具输出超过单次预算就 spill；
- mainagent 的 history 不超过窗口的 **40%**；
- planner 的 packet 不超过 **16k tokens / 64KB**。

### 16.4 mainagent：长会话 + 分段

**系统提示（稳定）**：

- 角色：人类接口、Goal 设定、验收、Hint/Constraint；
- 工具说明；
- 输出风格；
- 安全/范围策略。

**每轮动态上下文（user message）**：

- 当前 Goal + 验收标准；
- Constraint；
- 当前计划（Todo 摘要）；
- Top 5–10 findings；
- 预算/时间；
- 人类最新消息。

**不要放**：

- 完整探索图；
- 所有 Fact/Finding；
- worker 的完整日志；
- 原始工具输出。

**压缩**：

- `compaction.Config{ContextWindow, MaxOutputTokens, KeepRecent: 8}`；
- MicroCompact 清老工具结果；AutoCompact 在阈值前总结；
- Summarizer 用便宜模型；
- 413 时 Reactive 强制压缩并重试。

**分段**：

- 每 30–50 轮，或 Goal 变化、话题切换时，开新 segment；
- 写一份 handoff summary（Goal/Constraint/当前计划/Top findings）；
- transcript 按 segment 存，`Session.Resume` 只加载当前 segment；
- 旧 segment 仍可查，但不进上下文。

**关键状态重注入**：

每次压缩后，把以下内容重新注入：

```text
【不可丢状态】
Goal: ...
Constraints: ...
当前 Intent: ...
Todo: ...
预算: ...
```

否则压缩会丢约束，导致越界。

### 16.5 planner：每次 wake 新会话

**不 resume 旧 planner session**。每次 wake 都是新 session，靠 wake packet + Todo 恢复。

**wake packet 预算**：8–16k tokens，硬上限 64KB。

包含：

- Goal + 验收标准；
- Constraint；
- Todo（完整，通常很短）；
- Top 10 新 Fact（summary + confidence + id）；
- Top 5 新 Finding（title + severity + id）；
- 运行中的 Work（intent_id、worker、已运行时长）；
- 本轮 trigger（work_finished / finding / hint / …）；
- 预算；
- **计数**：`new_facts_total`、`new_findings_total`、`assets_total`、`open_intents`。

不包含：

- 完整图；
- 所有历史 Fact；
- worker 的完整对话；
- 原始工具输出。

**补读工具**：

- `get_node(id)`：看某条 Fact/Finding 的 detail/evidence；
- `list_facts(filter)`：按 intent/asset/时间过滤；
- `list_findings`；
- `get_work_output(work_id)`：看 worker 的结论；
- `node_detail` / `lineage`：看证据链。

**cold digest**：

- 详见 16.8；
- planner 只看 hot 节点 + digest 摘要；
- digest 有 `covers` 边，可下钻。

**输出**：

- `propose_intents` 结构化 JSON；
- `MaxTurns` 8–12；
- `MaxDuration` 2–3 分钟；
- 不聊天，不写长文。

### 16.6 worker：每次 work 新会话

**brief 预算**：3–6k tokens。

包含：

- Intent：summary / rationale / acceptance_criteria；
- 5–10 条最相关 Fact（按 anchor/derived_from/时间排序）；
- Constraint + scope；
- workdir；
- 可用工具；
- 输出要求（四类：资产/事实/发现/证据）。

不包含：

- planner 的完整思考；
- 其他 worker 的对话；
- 完整图；
- 历史 work 的日志。

**工具输出策略**：

| 工具 | 策略 |
|---|---|
| Bash | 超过 4k tokens → 写 `artifacts/cmd-<id>.log`，返回 head/tail + 路径 + 行数 |
| Read | 必须用 offset/limit；单次不超过 2k 行 |
| Grep | 最多 100 条匹配；返回 file:line + 1 行上下文 |
| WebFetch/HTTP | 解析关键字段；原始 body 写文件；上下文只留摘要 |
| MCP | 单次结果 > 4k tokens → 写文件 + 摘要 |

**压缩**：

- `compaction.Config`：MicroCompact + AutoCompact；
- `KeepRecent=8`；
- 工具结果被清掉后，仍可从 artifact 重读；
- 到 60% 窗口开始 MicroCompact，到阈值 AutoCompact。

**settlement**：

- 到时间/token/步数上限时，注入 settlement 提示：
  “停止探测，把已确认的资产/事实/发现/证据写回，然后 `finish`。”
- 不要求 summary、不建议、不规划。

### 16.7 approver：最小上下文

- 每次审批新 session；
- 只给：Intent summary、risk、dangerous_actions、scope、constraint；
- 不给历史对话、完整图、工具输出；
- 2–4k tokens；
- 策略服务能判的，不调 LLM；需要语义复核时用便宜模型，JSON 输出。

### 16.8 图级 cold digest（重点）

这是 HornedViper 相比普通“会话压缩”多出来的一层，参考 ARTEX 的 cold-digest：

**hot / cold 判定**：

- hot：live Intent（open/running/paused）及其所有祖先、live Intent 的直接子 Fact；
- cold：Fact / settled Intent，且连续 R 轮没有下游活跃分支；
- 参数：`R=6` 轮，`K=2`（最小折叠块），`N=20`（未压缩 cold 节点触发 minor），`M=8`（活跃 digest 触发 major），`cooldown=60s`。

**折叠**：

- 只折叠 Fact / settled Intent；
- 不折叠 Finding / Goal / Hint；
- 把连续 cold 块折叠成 `digest` 节点；
- `digest` 通过 `covers` 边指向成员；
- 成员仍在图里，可下钻，不丢证据。

**时机**：

- planner 每轮做一次轻量 maintain（更新 cold 标记），不调 LLM；
- 后台异步做 minor/major 压缩；
- 压缩前重新检查成员是否变 hot；变 hot 就不折叠。

**收益**：

- planner wake packet 不需要带所有历史 Fact；
- 老链条变成一条 digest 摘要；
- 需要细节时仍能 `node_detail` 下钻。

### 16.9 KV cache 策略

**稳定前缀**：

- 角色 system prompt；
- 工具 schema；
- 静态策略；
- 输出格式。

**动态后缀**：

- wake packet；
- 新 Fact/Finding；
- 工具结果；
- 当前时间/预算。

**规则**：

- 不要把时间戳、随机 ID、实时计数写进 system prompt；
- 同一角色的 system prompt 在任务内保持不变；
- 不要中途增删工具；用 deferred tools / skills 按需解锁；
- Norma 用 `DynamicBoundary` 标记可缓存前缀；
- wake packet 字段顺序固定，避免无意义 diff；
- 同一 task 的 planner 复用同一 system prompt，只换 user message。

### 16.10 记忆层级

```text
L0 当前 session 上下文
L1 任务图（Goal/Intent/Fact/Finding/Asset）
L2 planner Todo
L3 artifacts / 证据文件
L4 长期记忆 / skills（跨任务）
L5 llm_calls / workflow_events（审计）
```

只有 L0 进 prompt；L1–L3 通过工具读；L4 通过 skill/memory 注入；L5 只用于追溯，不进上下文。

### 16.11 指标与告警

| 指标 | 说明 |
|---|---|
| `context_tokens` | 每角色当前上下文 |
| `compaction_count` | 每任务压缩次数 |
| `cache_hit_rate` | KV cache 命中率 |
| `tool_spill_rate` | 工具输出落文件比例 |
| `planner_packet_tokens` | wake packet 大小 |
| `worker_brief_tokens` | worker brief 大小 |
| `time_to_plan_p95` | 事件到 planner 开始 |
| `task_makespan` | 任务总时长 |
| `cost_per_task` | token/费用 |

告警：

- 单任务压缩 > 10 次；
- packet > 16k tokens；
- 上下文 > 80% 窗口；
- 工具输出 spill > 50%；
- planner 连续 3 轮 0 Intent 但 Goal 未达成。

### 16.12 反模式

- ❌ 一个 session 跑完 mainagent + planner + worker；
- ❌ 把完整图/完整日志塞进 prompt；
- ❌ 把 Bash/HTTP 原始大输出留在上下文；
- ❌ 让 worker 写 summary/建议/下一步；
- ❌ 每轮改 system prompt；
- ❌ 压缩后不重注入 Goal/Constraint；
- ❌ 每次 planner 都 resume 旧会话；
- ❌ 用聊天历史当任务记忆；
- ❌ 给所有角色挂全部工具和 MCP schema。

---

## 17. 仍需确认

已确认的不再列出。剩下这些需要你定：

1. `pentest` 的 `dangerous_actions` 已收敛为 `delete / modify / credential_access`；`high/critical` 仍走人工。`privilege_escalation` / `lateral_movement` 是否也要单独列入，还是交给 `risk=high/critical`？
2. CTF flag 提交已定为“有提交途径就自动提交，没有就只记录”。提交途径的形态（API/token/命令行/适配器）需要你定一个。
3. v1 UI 第一批页面已同意：总览 + 任务详情（Plan/Sessions/Approvals）。是否再加一个独立“审批列表”页？
4. 登录已定为单用户；是否保留首次 setup 密码页？
5. planner 激活默认采用“有界批量 + 高信号直通”；`maxDelay` 已定为 CTF **2min**、pentest **5min**（见 5.4）。

---

## 附：一句话

> **mainagent 定 Goal，planner 出 Intent，approver 按模式放行，worker 只回写资产/事实/发现/证据；事件驱动、有界批量，SQLite 持久，UI 参考 ARTEX。**
