# 开发文档

> 最后更新：2026-10-21

## 1. 环境

- Go **1.26.3**
- Node 仅用于临时脚本/浏览器验证，项目本身不需要前端构建
- SQLite 由 `modernc.org/sqlite` 提供，不需要系统 sqlite 库
- 适配 macOS 本地开发

### Go 缓存

工作区沙箱只允许写当前目录，必须使用工作区缓存：

```bash
export GOPATH=$PWD/.gopath
export GOMODCACHE=$PWD/.gomodcache
export GOCACHE=$PWD/.gocache
export GOTOOLCHAIN=go1.26.3
```

`Makefile` 已经导出这些变量，直接 `make test` / `make serve` 也可以。

## 2. 常用命令

```bash
make test            # go test ./...
make serve           # 启动 :8787
make build           # bin/hornedviper
make fmt             # gofmt -w cmd internal
go vet ./...
```

单独跑测试：

```bash
go test ./internal/api -run TestName -v
go test ./...
```

## 3. 代码地图

```text
cmd/hornedviper/main.go      进程入口 / flag / 启动 API
internal/api/server.go       路由、Server 结构、Dashboard、任务页、设置
internal/api/features.go     发现、资产、审批、事件、聊天页面数据
internal/api/chat_agent.go   任务对话 mainagent
internal/api/conversations.go 新会话、session_agent、流式思考、工具循环
internal/api/chat_tools.go   Chat 工具集（只读 / 完整 / 权限过滤）
internal/api/jobs.go         后台 job API
internal/api/webtools.go     web_search / web_fetch
internal/api/uploads.go      附件保存、下载、路径校验
internal/api/events...       待审批/审批记录等
internal/kernel/engine.go    Task 状态机
internal/kernel/scheduler.go Planner 唤醒调度
internal/runtime/norma.go    Planner/Worker 真实运行时
internal/runtime/prompts.go  Planner/Worker system prompt
internal/runtime/httptool.go HTTPRequest + Traffic
internal/store/*.go          SQLite 访问
internal/store/schema.sql    初始 schema
internal/api/web/templates   Go 模板
internal/api/web/static      CSS / JS
```

## 4. 新增页面

1. 在 `internal/api/web/templates/` 新建 `xxx.html`，定义：

```html
{{define "content"}}
<section class="page-head">
  <div><h1>标题</h1></div>
</section>
...
{{end}}
```

2. 在 `internal/api/server.go` 的 `Handler()` 注册路由。
3. 在 `internal/api/features.go` 或新的 `xxx.go` 写 handler。
4. 使用 `s.render(w, "xxx.html", data)`。
5. 数据 struct 至少包含：

```go
Title string
Tab   string
```

`Tab` 用于侧边栏 active 状态。

6. 重启服务，因为模板是 `go:embed` 的。

## 5. 新增 API

在 `Handler()` 注册：

```go
mux.HandleFunc("/api/xxx", s.handleXxx)
```

- 返回 JSON 用 `writeJSON`
- 路径参数从 `r.URL.Path` 解析
- 错误统一：

```json
{"error": "..."}
```

- 不要忘记 `r.Method` 校验

## 6. 新增数据表

1. 修改 `internal/store/schema.sql`：

```sql
CREATE TABLE IF NOT EXISTS xxx (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ...
);
```

2. 旧库新增列使用 `ensureColumn`：

```go
if err := s.ensureColumn("table", "column", "TEXT NOT NULL DEFAULT ''"); err != nil {
    ...
}
```

3. 新 Store 方法放在 `internal/store/xxx.go`。
4. SQL 写操作需要 `s.mu.Lock()`。
5. 时间字段统一用 `nowMS()` / `msToTime()`。

## 7. 新增工具

工具使用 Norma 的 `tool.Build`：

```go
tool.Build(tool.Spec{
    Name: "xxx",
    Description: "...",
    Schema: map[string]any{...},
    Permissions: func(...) permission.Decision { return permission.Allowed() },
    Run: func(ctx context.Context, input json.RawMessage, tc *tool.ToolContext) (tool.Result, error) {
        ...
    },
})
```

- Chat 工具在 `internal/api/chat_tools.go`
- Worker 工具在 `internal/runtime/norma.go`
- 工具需要明确的 description，模型靠它决定是否调用
- 修改工具 schema 后要重启服务
- 工具输出尽量截断，避免撑爆上下文

## 8. 新增 Provider / 模型

Provider 配置在 `config.json`：

```json
{
  "providers": {
    "deepseek": {
      "format": "openai",
      "base_url": "https://api.deepseek.com",
      "model": "deepseek-flash",
      "api_key": "..."
    }
  }
}
```

- `format` 支持 `openai`、`openai-responses`、`anthropic`
- 设置页可以测试连接
- 不要把真实 key 写入文档/提交

## 9. 开发/调试技巧

### 看真实请求

```text
/llm-records
```

`llm_calls.request_json` 保存了 CompletionRequest，包含 System / Messages / Tools / MaxTokens。

### 看工具调用

- 任务：`/tasks/{id}?tab=sessions`
- LLM 记录中的 `tool_calls_json`
- 新会话：后端日志中的 `[conversation N] tool ...`
- 前台工具会显示在后台任务面板

### 看日志

```text
/logs
```

SSE 实时；SQLite 持久化。

### 测试临时服务

不要污染生产库：

```bash
rm -f /tmp/hv-test.db
make build
./bin/hornedviper -serve :8899 -db /tmp/hv-test.db -config /tmp/hv-test-config.json
```

## 10. 常见坑

1. **模板/CSS 不生效**：`go:embed`，必须重启 `make serve`。
2. **Go test 权限错误**：必须设置工作区 `GOCACHE`。
3. **任务对话刷新**：已改为局部轮询；不要再加 `location.reload`。
4. **思考状态被重置**：轮询替换消息时必须保留 `.think-box.collapsed`、`.think-step` 的 open 状态和滚动位置。
5. **mainagent 与 session_agent 混用**：任务对话是只读，不要给它加执行工具；新能力放 session_agent。
6. **工具轮次**：当前不设轮次上限；靠时间、上下文、用户停止收敛。
7. **数据库迁移**：`SeedDefaults` 使用 `INSERT OR IGNORE`；修改默认值要写显式迁移，且不要覆盖用户自定义值。
8. **前端 ID 冲突**：会话页和任务页都有 `chat-stop`、`chat-file-input` 等，但每个页面只加载一个。
9. **后台 job manager**：按会话 key 常驻 Server 内存；服务重启后列表丢失，输出文件还在。
10. **直接会话 LLM 记录**：目前没有写 `llm_calls`，不要误以为页面没有调用。

## 11. 测试策略

- 业务逻辑：`go test ./...`
- 模板：至少打开页面确认 200
- UI 改动：截图 1400x900 + 窄屏 1024x800
- 流式/轮询：用 Chrome CDP 模拟展开思考，等待轮询，确认状态不丢
- 后台任务：启动 `sleep` 任务，检查 `/api/jobs`、output、stop
- 不要在测试中删除生产 conversation/task

## 12. 提交前检查

```bash
gofmt -w cmd internal
go test ./...
go vet ./...
```

并确认：

- 生产会话没有被删除
- 服务已重启
- 关键页面返回 200
- 没有把 Token/Key 写入文档或日志

## 流程展示约定

- 架构/调度/任务流程优先输出图片，而不是 Mermaid 或大段文字图。
- 图片放在 `docs/`，例如 `docs/swarm_scheduling.png`、`docs/shard_scheduling.png`。
- 文字只保留简要说明、边界和当前实现状态。
