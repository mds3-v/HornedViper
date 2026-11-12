# API 与路由

> 最后更新：2026-10-21
> 当前没有登录/鉴权，默认只监听本机或受信网络。

## 1. 页面路由

| 路由 | 说明 |
|---|---|
| `/` | 仪表盘 |
| `/tasks` | 任务列表；POST 创建任务 |
| `/tasks/auto-ctf` | 自动 CTF：拉取未解决题目并创建任务 |
| `/tasks/{id}` | 任务详情 |
| `/findings` | 全部发现 |
| `/assets` | 全部资产 |
| `/approvals` | 待审批 |
| `/approval-records` | 审批记录 |
| `/events` | 事件 |
| `/traffic` | HTTP 流量 |
| `/chat?task={id}` | 任务对话 |
| `/new` | 新会话 |
| `/conversation/{id}` | 历史会话 |
| `/agents` | Agent 管理 |
| `/intercept-rules` | 拦截规则 |
| `/logs` | 后端日志 |
| `/llm-records` | LLM 调用记录 |
| `/settings` | 设置 |
| `/api` | API 说明页 |

## 2. 任务 API

### GET `/api/tasks`

返回最近 100 个任务。

```json
[
  {
    "id": 4,
    "title": "新任务",
    "mode": "ctf",
    "status": "completed",
    "goal_id": 12,
    "budget": {}
  }
]
```

### POST `/api/tasks`

```json
{
  "title": "任务名",
  "mode": "ctf",
  "goal": "目标描述"
}
```

注意：

- `mode` 不是 `pentest` 时统一按 `ctf`
- 该 JSON API 不接收 CTF 配置；带 CTF 配置的任务应通过页面创建（`/tasks` 表单）
- 返回创建后的公开任务（CTF token 已脱敏）

### GET `/api/tasks/{id}`

返回：

```json
{
  "task": {},
  "goal": {},
  "counts": {},
  "intents": [],
  "works": [],
  "findings": [],
  "events": [],
  "pending_approvals": []
}
```

### POST `/api/tasks/{id}/pause`

### POST `/api/tasks/{id}/resume`

### POST `/api/tasks/{id}/stop`

### POST `/api/tasks/{id}/approvals/{approvalID}`

```json
{
  "decision": "approved",
  "reason": "..."
}
```

### GET `/api/tasks/{id}/chat/messages`

返回任务对话消息 HTML fragment，用于局部轮询。

### POST `/api/tasks/{id}/chat/stop`

停止当前任务对话生成。

## 3. 新会话 API

### GET `/api/conversation/{id}/messages`

返回会话消息 HTML fragment：

```html
<div id="conversation-messages-inner" data-thinking="1">
  ...
</div>
```

### POST `/api/conversation/{id}/stop`

```json
{"stopped": true}
```

### 新建会话

页面 `POST /new`，multipart/form-data：

```text
text
reasoning_level   Off | Low | High | Max
permission_mode   read-only | workspace-write | full
files             可选，多文件
```

### 继续会话

页面 `POST /conversation/{id}`，字段同上。

## 4. 后台任务 API

### GET `/api/jobs?owner=conversation&id=4`

`owner`：`conversation` 或 `task`

返回：

```json
[
  {
    "id": "task_1",
    "kind": "bash",
    "command": "sleep 60",
    "description": "sleep 60",
    "status": "running",
    "exit_code": 0,
    "started_at": "2026-10-21T22:40:43+08:00",
    "ended_at": "",
    "backgrounded": true
  }
]
```

前台正在运行的工具也会以 `active: true` 返回：

```json
{
  "id": "live-1",
  "status": "running",
  "command": "运行命令 Python · ...",
  "active": true
}
```

### GET `/api/jobs/output?owner=conversation&id=4&job=task_1`

```json
{"output": "..."}
```

最多读取 64KB。

### POST `/api/jobs/stop?owner=conversation&id=4&job=task_1`

```json
{"stopped": true}
```

## 5. 附件

### GET `/attachments?task={id}&kind=uploads|chat&name=...`

任务附件。

### GET `/attachments?conversation={id}&kind=chat&name=...`

会话附件。

支持文件夹相对路径，例如：

```text
/attachments?task=1&kind=uploads&name=sub%2Fa.txt
```

路径会校验，拒绝 `..` 和目录穿越。

## 6. 日志

### GET `/api/logs/stream`

SSE：

```text
: connected

data: {"time":"10-21 22:14:30","text":"..."}
```

### GET `/api/health`

```json
{"ok": true}
```

实际返回由 handler 决定；用于健康检查。

## 7. CTFd

自动 CTF 不是独立 JSON API，而是页面表单：

```text
POST /tasks/auto-ctf
ctf_config=自然语言 CTFd 地址 + Token
```

提交器使用任务 budget 内的 CTF 配置，不属于 HTTP API。

## 8. 错误约定

- JSON API 错误：

```json
{"error": "message"}
```

- 页面错误：`http.Error` 纯文本
- 认证：当前无
- CSRF：当前无；仅适合单用户本地/受信环境
