# 当前架构

> 最后更新：2026-10-21

## 1. 总览

```text
Browser (Go templates + vanilla JS)
        │
        ▼
internal/api
  ├─ page handlers
  ├─ /api/* JSON handlers
  ├─ chat / conversation handlers
  ├─ background job UI API
  └─ SSE log stream
        │
        ├───────────────► internal/kernel
        │                   Task / Goal / Intent / Work 调度
        │                   planner 唤醒、worker lease、审批、goal met
        │
        ├───────────────► internal/runtime
        │                   NormaRuntime (真实模型)
        │                   FakeRuntime (测试)
        │                   HTTPRequest / tool recording
        │
        ├───────────────► internal/store
        │                   SQLite 单写者
        │
        ├───────────────► internal/platform
        │                   CTFd client
        │
        └───────────────► internal/submit
                            command submitter / per-task CTFd submitter
```

## 2. 代码分层

| 包 | 职责 |
|---|---|
| `cmd/hornedviper` | 进程入口、flag、启动 API / demo |
| `internal/api` | 页面、JSON API、聊天、日志、附件、任务控制、工具管理 |
| `internal/kernel` | 任务状态机、scheduler、planner/worker 调度、goal met |
| `internal/runtime` | 模型 Agent 运行时、Norma session、工具、LLM/trace 记录 |
| `internal/store` | SQLite schema、迁移、各领域读写 |
| `internal/domain` | Task / Goal / Intent / Work / Finding / Chat / Conversation 等类型 |
| `internal/platform` | CTFd challenge / submit 客户端 |
| `internal/submit` | Flag 提交策略：命令提交、按任务 CTFd 提交 |
| `internal/config` | `config.json`、Provider、运行时设置 |
| `internal/loghub` | 内存日志环 + SSE 订阅 + 持久化 sink |
| `internal/markdown` | goldmark + bluemonday 安全渲染 |
| `internal/rules` | 截拦规则匹配 |
| `internal/llmtest` | 本地 LLM 测试 Provider |

## 3. Task 工作流

### 3.1 层级

```text
Task
  └─ Goal
       └─ Intent（planner 唯一生成）
            └─ Work（1..N 个 worker 执行）
```

### 3.2 生命周期

1. 用户创建 Task，填写标题、模式、Goal。
2. `StartTaskWithBudget` 创建 `tasks` 行和 `goals` 行，启动 Engine。
3. Engine 通过 Scheduler 唤醒 Planner。
4. Planner 读取 Wake Packet，调用：
   - `propose_intents`
   - `propose_goal_met`
5. Engine 对 Intent 做审批：
   - `ctf`：自动批准
   - `pentest`：低风险自动，危险操作人工
6. Engine 创建 Works，worker 领取 lease。
7. Worker 在独立 session 中执行，只写四类结果：
   - `record_assets`
   - `record_facts`
   - `report_findings`
   - `attach_evidence`
8. Worker 调用 `finish(status)`。
9. Engine 持久化结果，触发下一轮 planner。
10. Planner 可调用 `propose_goal_met`，Engine 完成 Goal / Task。

### 3.3 Planner

System prompt：`internal/runtime/prompts.go`

关键约束：

- 唯一 Intent 生成者
- 不执行工具
- 不写 facts/findings/assets
- 不重复已有 Intent
- `execution_mode` 只允许 `single / shard / swarm`
- `dangerous_actions` 只允许 `delete / modify / credential_access`

User prompt 由 Wake Packet 组成：

```text
Task / Goal / Constraints / NewFacts / NewFindings / Hints
OpenIntents / RunningWorks / Todo / Trigger / Budget
```

### 3.4 Worker

System prompt：`internal/runtime/prompts.go`

Worker 工具：

```text
Read / Write / Edit / MultiEdit / LS / Glob / Grep / Bash / Sleep
HTTPRequest
record_assets / record_facts / report_findings
attach_evidence / finish
```

WebFetch 在 worker 中受限；HTTP 请求应使用 `HTTPRequest`，这样原始报文会进入 Traffic。

Worker 的 working directory：

```text
artifacts/task-{id}/work-{id}/
```

## 4. Chat 与 Session

系统有两套对话，不能混用。

### 4.1 任务对话 `/chat?task=id`

角色：`mainagent`

- 只读任务解释器
- 可看任务状态、发现、事实、资产、Work、LLM 摘要
- 工具：
  - `get_work_trace`
  - `search_task`
  - `get_llm_call`
  - `read_attachment`
- 不执行 Bash / Python / Web
- 保持原始 mainagent 设计

实现：

- `internal/api/chat_agent.go`
- `internal/api/chat_tools.go`
- `internal/api/chat_messages_handler.go`

### 4.2 新会话 `/new`、`/conversation/{id}`

角色：`session_agent`

- 独立 conversation，不绑定 Task
- 工具：
  - Bash / Python
  - Read / Write / Edit / MultiEdit
  - Glob / Grep / LS
  - read_image
  - web_search / web_fetch
  - todo_write
  - job_list / job_output / job_kill
  - read_attachment
- 支持推理等级：
  - Off / Low / High / Max
- 支持操作权限：
  - 仅可查看 / 工作区内修改 / 完全权限

实现：

- `internal/api/conversations.go`
- `internal/api/chat_tools.go`
- `internal/api/jobs.go`
- `internal/api/webtools.go`
- `internal/api/uploads.go`

## 5. 工具矩阵

| 工具 | Task worker | Task mainagent | Session agent |
|---|---:|---:|---:|
| Read / Write / Edit | 是 | 否 | 是 |
| Bash | 是 | 否 | 是 |
| Python | 否（可用 Bash） | 否 | 是 |
| HTTPRequest | 是 | 否 | 否 |
| WebSearch / WebFetch | 否 | 否 | 是 |
| read_image | 否 | 否 | 是 |
| 任务图查询 | 否 | 是 | 否（可有限） |
| 后台 job | 否 | 否 | 是 |
| todo_write | 否 | 否 | 是 |

## 6. 权限等级

会话字段：

```text
conversations.permission_mode
```

取值：

```text
read-only         仅可查看
workspace-write   工作区内修改（默认）
full              完全权限
```

- `read-only`：工具列表直接移除 Bash / Python / Write / Edit / Todo / Job
- `workspace-write`：
  - Write / Edit / MultiEdit 路径限制在 `artifacts/conversation-{id}/workspace`
  - Bash / Python 以工作区为 cwd
  - 注意：目前不是 OS 级沙箱
- `full`：不做工作区路径限制

## 7. 后台任务

每个会话/任务对话有独立 `tool.Manager`：

```text
conversation-{id}-chat
task-{id}-chat
```

- Bash 支持 `run_in_background`
- Python 支持 `run_in_background`
- UI 面板通过 `/api/jobs` 拉取
- 前台工具运行期间也会显示为「运行中」
- job 输出通过 `/api/jobs/output`
- 停止通过 `/api/jobs/stop`

## 8. Flag 提交

### 通用命令

```text
config.SubmitCommand
```

### CTFd

每个 Task 的 budget 中保存：

```json
{
  "ctf": {
    "platform": "ctfd",
    "url": "https://ctfd.example.com",
    "token": "...",
    "challenge_id": 123
  }
}
```

`TaskCTFd` 使用任务自己的配置提交，不依赖全局 CTFd 配置。

自动 CTF：

- 任务页「自动 CTF」
- 输入自然语言配置
- 拉取所有未解决题目
- 为每题创建 Task
- 写入 `external_tasks` 去重

当前不自动启动/停止题目容器。

## 8.1 工具管理

`/tools` 提供内置工具目录和启用开关：

- `managed_tools`：内置工具 + 自定义 HTTP 工具
- `tool_categories`：工具分类
- scope：`session` / `task-chat` / `worker` / `planner`
- 会话与任务对话构建工具时过滤
- worker / planner 通过 `NormaConfig.ToolEnabled` + `CustomTools` 过滤/加载
- `finish`、`record_*`、`propose_*` 等核心工具不可关闭或删除
- 自定义工具目前只支持 HTTP，`{input}` 会替换 URL/请求头/请求体

## 9. 持久化

主库：`hornedviper.db`

关键表：

```text
tasks / exploration_nodes / exploration_edges
assets / asset_edges / exploration_anchors
works / approvals / workflow_events / activities
llm_calls / artifacts / task_constraints / planner_todos
task_scope / flag_submissions
agents / intercept_rules / traffic
chat_messages / chat_summaries
logs
external_tasks
conversations / conversation_messages / chat_todos
```

WAL 模式，单写者 `Store.mu`。

## 10. 日志与 LLM 记录

### 后端日志

```text
log.Printf
  → loghub 内存环（5000）
  → SSE /api/logs/stream
  → SQLite logs（最多 50000）
```

日志页显示最近 500 条。

### LLM 记录

`llm_calls` 记录：

- role / provider / model
- request_json / response_json
- thinking / tool_calls_json
- usage_json / latency_ms / error
- request_hash

任务 worker/planner/mainagent 的调用会写入。
直接会话（session_agent）的调用也会写入 `llm_calls`，使用 `conversation_id` 区分。

## 11. 运行时限制

- 对话：180 秒
- Planner：5 分钟
- Worker：20 分钟
- 工具轮次：不限制
- MaxTokens：256000
- Planner/Worker Compaction：ContextWindow 200000，MaxOutput 8000，KeepRecent 8
- 新会话：22 条后自动滚动摘要，保留最近 10 条
- 任务对话：已有滚动摘要
- 任务 Planner/Worker：Norma compaction
- 工具输出截断：聊天 12k，Python 64KB，Web 32KB，附件 64KB 等

详见 `docs/PROGRESS.md`。
