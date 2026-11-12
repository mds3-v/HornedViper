# HornedViper

HornedViper 是 [WORKFLOW_DESIGN.md](WORKFLOW_DESIGN.md) 的实现：
Go + Norma + SQLite，事件驱动，LLM 负责判断，工作流负责状态、图、审批、预算和日志。

当前版本：**v1.0**。已完成 Phase 3 的 CTF 调度、swarm/shard、隔离、冲突与重试策略，以及报告导出、流量分组和任务详情报告页。

## 已实现

### 内核

- `Task → Goal → Intent → Work` 领域模型与状态机
- SQLite schema：任务、探索图、资产、Work、审批、事件、Activity、LLM Call、证据、约束、Todo、flag 提交
- 事件驱动调度器：
  - 高信号事件立即唤醒
  - 普通事件 debounce
  - `MaxDelay` 上限（CTF 2min / pentest 5min）
  - planner 运行期间只保留一个 dirty wake
  - 过滤 planner 自身产生的内部事件，避免自触发
- Engine：
  - Goal → Planner → 审批 → Worker → 四类回写 → Goal 验收
  - `ctf` 模式全自动审批
  - `pentest` 模式危险 Intent 进入 pending
  - 单写者、SQLite WAL、单进程
- 持久化 approval 队列和 `flag_submissions` 去重

### Runtime

- `FakeRuntime`：无 Fact → recon；有 Fact → exploit；有 Finding → Goal met
- `NormaRuntime`：
  - `agentcore.Session` 驱动 planner / worker
  - 流式事件写入 `activities`：prompt / thinking / assistant_text / tool_use / tool_result / usage / result
  - planner 只有 `propose_intents`
  - worker 有默认文件/Shell 工具 + `record_assets` / `record_facts` / `report_findings` / `attach_evidence` / `finish`
  - worker 只输出资产、事实、发现、证据
  - Norma compaction 已配置
- 默认 provider：**DeepSeek**；同时支持 ChatGPT/OpenAI、Claude/Anthropic、Zhipu/GLM
- 配置来自 `config.json`，也可以在 UI 的 Settings 页编辑

### UI

`-serve :8787` 启动 Go HTTP UI，信息架构参考 ARTEX：

- Dashboard：任务、运行中、发现、资产、待审批、最近事件
- Task 详情：
  - Overview
  - Plan（Goal / Intent）
  - Sessions（Work / Activity / **LLM Trace**）
    - 真实模型运行时显示 `prompt` / `thinking` / `assistant_text` / `tool_use` / `tool_result` / `usage` / `result`
    - 流式 token 会合并成一条完整的思考/模型文本记录，不再每个 token 一行
    - 每行可展开 `detail`，查看完整 prompt、思考、工具参数和工具输出
    - fake runtime 不产生这些事件
  - Approvals（Intent 审批）
  - Findings
  - Assets
  - Events
- 左侧栏新增：
  - **LLM 记录**：每次模型调用的请求/响应/思考/工具/token/延迟/错误
  - **对话**：mainagent 结合任务状态、发现、事实、资产异步回答，展示思考过程和回答；消息同时作为 hint 注入 planner
  - **流量**：只记录 worker 发起的 HTTP(S) 请求（WebFetch / Bash curl/wget），其他工具调用不进入流量；支持展开请求与响应
  - **Agent 管理**：内置 mainagent / planner / worker / approver / swarm_lead；可新增自定义 Agent
  - **拦截规则**：默认内置破坏性命令、数据修改、凭据访问、反弹 Shell、外发数据规则；按优先级匹配，ask/deny 会阻止工具调用
  - **审批记录**：记录 approver / 人工的意图审批历史
  - **后端日志**：内存日志缓冲 + SSE 实时刷新
- 任务控制：**暂停 / 继续 / 停止**
  - 暂停：取消当前 planner/worker，状态变为 `paused`
  - 继续：重新创建 engine，状态回到 `running`
  - 停止：取消执行并标记 `cancelled`，不可恢复
  - API：`POST /api/tasks/{id}/pause|resume|stop`
- Settings：供应商列表 + 运行配置
  - 供应商卡片可**拖动排序**，点击进入详情
  - 每个供应商有 **Test** 按钮，详情页也有 **Test**
  - 新建供应商先进入**预设模板库**（官方 / 中国官方），选择模板后填写模型、Base URL、API Key、上游协议
  - 详情页支持设为默认、删除、保存
  - Test 使用当前表单值，不必先保存；成功显示模型、延迟和回复
- 单用户；无登录页
- 流量只采集 WebFetch 和 Bash curl/wget 的 HTTP(S) 请求，不采集文件读写等工具调用

### CTF 提交 / 审批 API

- `-submit-cmd` 或 `HV_SUBMIT_CMD`：用户提供 flag 提交命令
  - `{flag}` 占位符会被 shell-quote 后替换
  - `HV_FLAG` / `HV_TASK_ID` / `HV_FINDING_ID` 环境变量可用
  - exit 0 = accepted；非 0 = rejected
- 自动提交只在 `ctf` 模式 + finding 中识别到 `flag{...}` + 用户提供命令时发生
- `flag_submissions` 按 task + flag hash 去重
- Intent 审批 API：
  - `POST /api/tasks/{id}/approvals/{approvalID}`
  - body: `{"decision":"approved|rejected","reason":"..."}`

### 暂停 / 停止演示

```bash
go run ./cmd/hornedviper -serve :8787 -db /tmp/hv.db -runtime fake -fake-delay 3s
```

打开任务详情，在任务运行期间点击「暂停」；再点「继续」；最后点「停止」。

### 审批流程验证

```bash
go run ./cmd/hornedviper -serve :8787 -db /tmp/hv.db -runtime fake -fake-risky
```

1. 新建一个 `pentest` 任务；
2. fake runtime 的 recon 是 low risk，自动通过；
3. exploit 是 high risk + `modify`，进入 Approvals；
4. 在 UI 的 Approvals 页或 API 批准；
5. engine 被 `task.resumed` 事件唤醒，执行该 Intent，任务完成。

命令：

```bash
curl -X POST http://127.0.0.1:8787/api/tasks/1/approvals/1 \
  -H 'Content-Type: application/json' \
  -d '{"decision":"approved","reason":"ok"}'
```

## 运行

```bash
export GOPATH=$PWD/.gopath
export GOMODCACHE=$PWD/.gomodcache
export GOCACHE=$PWD/.gocache
export GOTOOLCHAIN=go1.26.3

go test ./...
```

### Demo（Fake Runtime）

```bash
make run
```

### UI

```bash
make serve
# http://127.0.0.1:8787
```

如果还没有配置 API key，Dashboard 会提示；直接打开 `/settings` 填写即可，新任务立即使用新配置。

### 真实模型（默认 DeepSeek）

```bash
cp config.example.json config.json
# 编辑 config.json 里的 providers.deepseek.api_key，
# 或者启动 UI 后在 /settings 页面填写。

make serve
# http://127.0.0.1:8787/settings
```

`config.json` 默认：

- `runtime`: `norma`
- `default_provider`: `deepseek`
- `providers.deepseek`: OpenAI 兼容，`https://api.deepseek.com/v1`，`deepseek-chat`
- `providers.openai`: ChatGPT/OpenAI，`https://api.openai.com/v1`
- `providers.anthropic`: Claude，`https://api.anthropic.com`，Anthropic 格式
- `providers.zhipu`: 智谱 GLM，`https://open.bigmodel.cn/api/paas/v4`

| Provider | config key | 协议 | 默认 base URL | 默认模型 |
|---|---|---|---|---|
| DeepSeek（默认） | `deepseek` | OpenAI | `https://api.deepseek.com/v1` | `deepseek-chat` |
| ChatGPT / OpenAI | `openai` | OpenAI | `https://api.openai.com/v1` | `gpt-4o` |
| Claude / Anthropic | `anthropic` | Anthropic | `https://api.anthropic.com` | `claude-sonnet-4-20250514` |
| Zhipu / GLM | `zhipu` | OpenAI | `https://open.bigmodel.cn/api/paas/v4` | `glm-4.5` |

API key 也可以先用环境变量兜底：`DEEPSEEK_API_KEY`、`OPENAI_API_KEY`、`ANTHROPIC_API_KEY`、`ZHIPU_API_KEY`。CLI 还支持 `-provider`、`-api-key`、`-base-url`、`-model`、`-format` 覆盖；`-provider` 支持别名 `chatgpt` / `claude` / `glm` / `智谱`。

`config.json` 默认被 `.gitignore` 忽略；API key 以 0600 权限保存在本地。

### CTF 提交命令

```bash
go run ./cmd/hornedviper -db /tmp/hv.db -mode ctf \
  -submit-cmd 'curl -s -X POST https://ctf.example/api/submit -d "flag=$HV_FLAG"'
```

或：

```bash
export HV_SUBMIT_CMD='./scripts/submit.sh'
```

## 目录

```text
cmd/hornedviper/          CLI + HTTP server
config.example.json        provider / runtime 配置示例
contracts/                JSON Schema
internal/api/             HTTP server + templates + CSS
internal/domain/          类型、状态机、校验、Submitter 接口
internal/store/           SQLite schema + 持久化
internal/kernel/          Scheduler + Engine
internal/runtime/         Runtime 接口 + Fake + Norma
internal/config/          config.json 加载/保存、provider 预设
internal/submit/          CommandSubmitter
WORKFLOW_DESIGN.md        设计文档
DESIGN.md                 早期 CTF 专用设计（部分已被覆盖）
```

## API

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/health` | 健康检查 |
| GET | `/api/tasks` | 任务列表 |
| POST | `/api/tasks` | 创建任务 `{title,mode,goal}` |
| GET | `/api/tasks/{id}` | 任务详情 |
| POST | `/api/tasks/{id}/approvals/{approvalID}` | 审批 Intent |

HTML：

- `/`
- `/tasks/{id}?tab=overview|plan|sessions|approvals|findings|assets|events`

## 新会话

- 左侧「新会话」用于不绑定任务的直接对话
- 支持多会话历史、附件、粘贴/拖拽上传
- 会话和消息持久化在 SQLite 的 `conversations` / `conversation_messages` 表，重启不会清空
- 点击左侧历史会话可继续追问，不需要重新提问
- 对话框沿用 DeepSeek Harness 风格：附件卡片、`+`、模型名、圆形发送按钮
- 思考过程通过流式 reasoning 实时写入并展开显示
- 对话主 Agent 可使用 Bash、Python、Read/Write/Edit、Glob/Grep 等基础工具
- 新增 `web_search`、`web_fetch`、`read_image`、`job_list/job_output/job_kill`、`todo_write`
- 对话输入框支持停止当前生成；后台任务面板可直接查看输出和停止任务
- 对话与任务的模型输出上限为 `256000`
- 新会话支持推理等级：`Off` / `Low` / `High` / `Max`，默认 `High`
- 推理等级随会话持久化，后续继续对话沿用；发送新消息时可修改

## 文件上传

- 新建任务只有一个「上传」区域：支持多文件、文件夹和拖拽，保存到 `artifacts/task-{id}/uploads/`，文件夹保留相对目录结构
- 对话支持粘贴图片/文件、拖拽上传，保存到 `artifacts/task-{id}/chat/`
- 任务上传文件会写入任务 budget，worker 的提示词中会列出绝对路径
- 聊天附件会在主 Agent 上下文中列出；小文本附件直接内联，大文件可用 `read_attachment`
- 文件通过 `/attachments?task=...&kind=uploads|chat&name=...` 下载

## CTFd 平台

- 不再使用全局 CTFd 配置
- 任务页「自动 CTF」：输入 CTFd 地址和 Token，自动拉取所有未解决题目、创建任务并开始解题
- 每题使用自己的 CTF 配置提交 Flag
- 新建任务选择 CTF 模式时，必须填写该任务的 CTF 配置（自然语言，自动识别地址/Token/题目 ID）
- 自然语言示例：`CTFd 地址：https://...`、`Token：...`、`题目 ID：123`
- 配置保存在任务 budget 中
- finding 中出现 `flag{...}` / `FLAG{...}` 等形式时，使用该任务自己的 CTFd 配置提交
- 当前不做容器启动/停止（CTFd 标准 API 没有统一容器接口）

## 当前边界

- 已用 DeepSeek 真实跑通一次完整循环：`Goal → Intent → Work → HTTPRequest → finding → propose_goal_met → completed`
- 已有 CTFd challenge/submit adapter；rCTF 和其他平台尚未接入
- CTFd 容器启动/停止尚未接入
- `llm_calls` 已写入：请求/响应/thinking/tool_calls/usage/latency/error，并提供 `/llm-records` 页面
- 还没有接 cold digest / 图级压缩 / tool spill 策略
- 没有 swarm
- UI 是 Go 模板 + 原生 CSS，信息架构参考 ARTEX，不是 Next.js
- 单用户、单进程、SQLite 单写者

## 下一步

1. 自动 CTF 定时轮询 / 容器生命周期
2. cold digest + tool spill + worker brief 预算
3. swarm（单 Intent 多 lane + 共享黑板 + quorum）
4. UI 打磨（参考 ARTEX 的力导向图、会话流、审批卡片）

## 开发文档

当前实现文档在 `docs/`：

- `docs/HANDOFF.md` — 新对话交接摘要
- `docs/ARCHITECTURE.md` — 架构、角色、工具、数据流
- `docs/DEVELOPMENT.md` — 开发环境、代码地图、扩展方式
- `docs/API.md` — 页面路由、HTTP API、附件、SSE
- `docs/OPERATIONS.md` — 启动、配置、SQLite、日志、备份
- `docs/PROGRESS.md` — 进度、已知问题、路线图、决策记录
- `docs/CHANGELOG.md` — 重要变更记录

## 注意

- Go 版本：`go.mod` 现在是 `go 1.26.3`，Norma v0.4.3 需要它；`GOTOOLCHAIN=go1.26.3` 已下载。
- SQLite 是单写者模型；当前用单连接 + 互斥锁。
- 审批只发生在 Intent 层；tool 层不做逐次审批。
- 日志不脱敏，按设计原文存储。
