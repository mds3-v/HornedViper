# 新对话交接文档（HANDOFF）

> 最后更新：2026-10-21
> 适用对象：接手 HornedViper 开发的新 AI 会话 / 新工程师

## 0. 一句话

HornedViper 是一个已经能真实调用 DeepSeek 完成 `Task → Goal → Intent → Work → Finding` 循环的 Go 工作流 Agent，当前重点从“工作流正确”转向“会话体验、工具能力、CTF 自动化和稳定性”。

## 1. 启动与验证

```bash
cd /Users/mds3/Documents/ctf-agent-vibecoding
export GOPATH=$PWD/.gopath
export GOMODCACHE=$PWD/.gomodcache
export GOCACHE=$PWD/.gocache
export GOTOOLCHAIN=go1.26.3

go test ./...
go vet ./...
make serve
```

- 服务：`http://127.0.0.1:8787`
- 数据库：`hornedviper.db`
- 配置：`config.json`
- 默认模型：`deepseek-flash`
- 单用户、单进程、SQLite 单写者

不要依赖固定 PID；PID 每次 `make serve` 都会变化。

## 2. 绝对不要做的事情

1. 不要对生产库执行测试性 `DELETE`、`DROP`、`RESET`。
2. 不要为了测试新建生产会话后再删除；使用 `/tmp` 临时 DB。
3. 不要修改 `agents` 表中用户自定义的 Agent 提示词。
4. 不要把 CTF Token、API Key 写进文档、日志或提交记录。
5. 不要在没有重启服务的情况下断言模板/CSS/Go 改动已生效；模板和静态文件是 `go:embed` 的。

## 3. 当前角色设计

### Task 工作流

```text
Task → Goal → Intent → Work
```

- `mainagent`：任务对话解释者，只读，不执行工具。
- `planner`：唯一 Intent 生成者。
- `worker`：执行单个 Intent，只写 assets / facts / findings / evidence。
- `approver`：Intent 审批。

### 新会话

`session_agent` 是独立角色：

- 不绑定 Task
- 独立 conversation
- 可使用 Bash / Python / 文件 / Web / 图片 / 后台任务工具
- 支持推理等级：`Off / Low / High / Max`
- 支持操作权限：`仅可查看 / 工作区内修改 / 完全权限`

`mainagent` 和 `session_agent` 的种子定义在：

```text
internal/store/agents.go
```

新会话实际使用 `session_agent` 的 system prompt；任务对话使用 `internal/api/chat_agent.go` 中的 `mainAgentPrompt`。

## 4. 已经实现的关键能力

- 真实模型闭环：planner / worker / goal met / completed
- Dashboard、任务、发现、资产、流量、审批、事件、Agent、规则、设置
- LLM 调用记录：请求、响应、thinking、tool_calls、usage、latency
- 后端日志：SSE 实时 + SQLite 持久化 + 跨重启保留
- Dashboard Token 统计
- Task 上传文件 / 文件夹，保留相对路径
- 新会话 / 任务对话：Markdown、附件卡片、粘贴/拖拽、停止生成
- 思考过程流式写入，DSH 风格时间线，默认收起，展开状态在轮询中保持
- 后台任务：job_list / job_output / job_kill + UI 面板
- 前台工具也会显示在后台任务面板
- Web：web_search / web_fetch
- 图片：read_image（尺寸/格式/OCR）
- CTFd：自动拉取未解决题目 → 创建任务 → 每题独立 CTF 配置 → 自动提交 flag
- 新会话权限模式持久化
- 任务对话主 Agent 保持只读

## 5. 当前已知问题 / 待办优先级

### P0

1. 新会话和任务对话**没有自动上下文压缩**；超长对话可能撞到 180 秒或上下文窗口。
2. `workspace-write` 目前只对 Write / Edit / MultiEdit 做路径校验；Bash / Python 仍是本机进程，不是 OS 级沙箱。
3. Web Search 依赖 Bing（DuckDuckGo 作为备用），网络环境可能影响结果。
4. CTFd 没有容器启动/停止能力（标准 API 不统一）。
5. 自动 CTF 是点击一次拉取，不是后台定时轮询。

### P1

2. 后台 job 管理按进程内存保存，服务重启后历史 job 列表丢失；输出文件仍在磁盘。
3. 任务对话有只读查询工具和附件，但没有执行工具；这是有意保持 mainagent 只读设计。
4. 文件/文件夹上传没有大小上限，但 multipart 内存阈值是 32MB。
5. 日志页面只展示最近 500 条（DB 保留 50000 条）。
6. 任务 worker 没有独立 Web Search 工具；目前只有 chat/session agent 有。

### P2

- rCTF / 其他平台适配器
- `max_minutes` 限制已移除；只有 `hard_max_minutes` 可选
- cold digest / tool spill / context compaction 策略
- swarm（单 Intent 多 lane + quorum）
- 更完整的会话列表 / 搜索 / 归档

## 6. 最近的重要修复（避免回退）

- 对话流式思考：使用 `prov.Stream`，每 250ms 写入 DB
- 思考时间线不能截断：`thinkingSteps` 已移除 60 步上限
- 轮询替换消息时保留思考展开状态和滚动位置
- 新会话 / 任务对话工具轮次不设上限
- Planner 超时 5 分钟；Worker 20 分钟；对话 180 秒
- 任务对话 60 秒内不会再整页刷新；改为局部轮询
- 新旧会话区分：mainagent 只读，session_agent 工具化
- 权限等级会被持久化到 `conversations.permission_mode`

## 7. 推荐的新对话开场提示

```text
你在 /Users/mds3/Documents/ctf-agent-vibecoding 开发 HornedViper。
先读 docs/HANDOFF.md、docs/ARCHITECTURE.md、docs/DEVELOPMENT.md、docs/PROGRESS.md。
遵守：
1. 不删除生产会话/任务数据，测试用 /tmp 临时 DB；
2. 修改后重启 make serve 并验证页面；
3. 不要改变 mainagent 的只读设计，新能力放到 session_agent；
4. 完成后运行 go test ./... 和 go vet ./...。
当前优先事项：<填写本次任务>。
```
