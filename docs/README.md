# HornedViper 文档索引

HornedViper 是一个 Go + Norma 的安全测试工作流 Agent，当前以 CTF 为主要落地场景，也支持渗透测试模式。

## 新对话先读这里

如果你是一个新的 AI 开发会话，按这个顺序读：

1. `docs/HANDOFF.md`：当前状态、约束、下一步
2. `docs/ARCHITECTURE.md`：系统结构和角色
3. `docs/DEVELOPMENT.md`：开发、调试、测试约定
4. `docs/PROGRESS.md`：已完成内容、已知问题、路线图

历史设计文档仍然保留：

- `DESIGN.md`：早期 CTF 插件设计草案
- `WORKFLOW_DESIGN.md`：Task → Goal → Intent → Work 工作流设计

它们描述的是设计意图，不一定等于当前实现。当前实现以 `docs/` 下的工程文档和代码为准。

## 文档列表

| 文档 | 用途 |
|---|---|
| `docs/HANDOFF.md` | 新对话交接摘要 |
| `docs/ARCHITECTURE.md` | 当前架构、角色、工具、数据流 |
| `docs/DEVELOPMENT.md` | 开发环境、代码地图、扩展方式、测试 |
| `docs/API.md` | 页面路由、HTTP API、SSE、附件 |
| `docs/OPERATIONS.md` | 启动、配置、SQLite、日志、备份 |
| `docs/PROGRESS.md` | 阶段进度、已知问题、路线图、决策记录 |
| `docs/CHANGELOG.md` | 重要变更记录 |

## 快速启动

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

默认地址：

```text
http://127.0.0.1:8787
```

默认数据库：

```text
hornedviper.db
```

## 当前最重要的事实

- 所有持久化都在单文件 SQLite，单用户、单进程。
- 日志和会话不会因为服务重启自动清空。
- 任务对话的主 Agent 保持只读设计；新会话使用独立的 `session_agent`。
- 会话支持推理等级和 AI 操作权限等级。
- 新会话支持 Bash / Python / 文件 / Web / 图片 / 后台任务工具。
- 工具调用轮次没有上限；仍有时间、上下文和输出截断限制。
- 不要对生产库执行测试性删除。验证使用 `/tmp` 下的临时数据库。
