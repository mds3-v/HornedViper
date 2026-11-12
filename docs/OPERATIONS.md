# 运行与运维

> 最后更新：2026-10-21

## 1. 启动

```bash
cd /Users/mds3/Documents/ctf-agent-vibecoding
make serve
```

等价于：

```bash
go run ./cmd/hornedviper \
  -serve :8787 \
  -db "$PWD/hornedviper.db"
```

访问：

```text
http://127.0.0.1:8787
```

## 2. 构建

```bash
make build
./bin/hornedviper -serve :8787 -db ./hornedviper.db
```

## 3. 配置

默认读取 `config.json`：

```json
{
  "runtime": "norma",
  "default_provider": "deepseek",
  "providers": {
    "deepseek": {
      "format": "openai",
      "base_url": "https://api.deepseek.com",
      "model": "deepseek-flash",
      "api_key": "..."
    }
  },
  "artifact_dir": "artifacts",
  "submit_command": ""
}
```

字段：

| 字段 | 说明 |
|---|---|
| `runtime` | `norma` 或 `fake` |
| `default_provider` | 默认 Provider key |
| `providers` | Provider 配置 |
| `artifact_dir` | 附件、证据、工作目录根 |
| `submit_command` | 可选通用 flag 提交命令 |

Provider `format`：

```text
openai
openai-responses
anthropic
```

设置页支持编辑/测试 Provider。不要把 API Key 写入文档或提交记录。

## 4. 数据库

```text
hornedviper.db
hornedviper.db-wal
hornedviper.db-shm
```

- SQLite WAL
- 单进程、单写者
- 服务运行中不要用另一个进程长时间写库
- 备份前建议停止服务，或使用 SQLite backup API

备份：

```bash
cp hornedviper.db hornedviper.db.bak
```

更稳妥：

```bash
sqlite3 hornedviper.db ".backup '/path/to/hornedviper-$(date +%Y%m%d-%H%M%S).db'"
```

恢复：

1. 停止服务
2. 替换 `hornedviper.db`
3. 删除 `hornedviper.db-wal` / `-shm`（或让 SQLite 自行恢复）
4. 启动服务

## 5. Artifacts 目录

```text
artifacts/
  task-{id}/
    uploads/          任务创建时上传的文件
    chat/             任务对话附件
    chat-workspace/   任务对话工作目录
    work-{workID}/    worker 工作目录
      cmd/            工具输出 spill
      evidence/       证据文件
  conversation-{id}/
    chat/             会话附件
    workspace/        会话工作目录
```

附件下载：

```text
/attachments?task=1&kind=uploads&name=file.txt
/attachments?task=1&kind=uploads&name=sub%2Ffile.txt
/attachments?conversation=4&kind=chat&name=file.txt
```

## 6. 日志

### 页面

```text
/logs
```

- SSE 实时追加
- 页面显示最近 500 条
- 自动滚动
- 可手动停留查看

### SQLite

```text
logs 表
```

- 默认最多 50000 条
- 服务启动时裁剪

### 应用日志

`make serve` 的 stderr 同时写入终端和 loghub。

## 7. 健康检查

```bash
curl -s http://127.0.0.1:8787/api/health
```

## 8. 停止服务

如果使用 `make serve` 前台运行，`Ctrl-C`。

如果使用后台任务管理：

```bash
lsof -nP -iTCP:8787 -sTCP:LISTEN
kill <PID>
```

不要在不确认 PID 的情况下 kill。

## 9. 临时测试环境

不要用生产库测试：

```bash
rm -f /tmp/hv-test.db
cat > /tmp/hv-test-config.json <<'JSON'
{
  "runtime": "fake",
  "default_provider": "deepseek",
  "providers": {
    "deepseek": {
      "format": "openai",
      "base_url": "https://api.deepseek.com",
      "model": "deepseek-flash",
      "api_key": ""
    }
  },
  "artifact_dir": "/tmp/hv-test-artifacts"
}
JSON

./bin/hornedviper -serve :8899 \
  -db /tmp/hv-test.db \
  -config /tmp/hv-test-config.json
```

## 10. 重置

不要直接删除生产数据库，除非确认要清空所有任务、会话、日志、Agent。

若要保留结构但清空数据，最好先备份，并按表清理。

## 11. 安全边界

- 当前没有登录和 CSRF 防护，仅适合本机/受信网络。
- Agent 的 `workspace-write` 不是 OS 级沙箱：
  - Write/Edit/MultiEdit 有路径校验
  - Bash/Python 仍是本机进程
- Bash / Python 可以访问本机文件系统；不要在不可信输入下开放服务。
- Web 内容作为不可信数据处理。
- 会话 Token 不会出现在公开 Task API 的 budget 中（已脱敏），但仍保存在 SQLite 和附件目录。
