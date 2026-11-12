package api

// toolInfo describes one built-in tool in the management UI. The catalog is
// intentionally host-owned: tools are compiled into the agent, not installed
// at runtime, so the manager can only switch existing tools on or off.
type toolInfo struct {
	Scope       string
	ScopeTitle  string
	Name        string
	Display     string
	Description string
	Category    string
	Essential   bool
}

const (
	toolScopeSession  = "session"
	toolScopeTaskChat = "task-chat"
	toolScopeWorker   = "worker"
	toolScopePlanner  = "planner"
)

var toolCatalog = []toolInfo{
	// Session agent (new conversations).
	{toolScopeSession, "会话 Agent", "Bash", "Bash", "执行 shell 命令，支持后台运行、超时和输出读取。", "执行", false},
	{toolScopeSession, "会话 Agent", "Python", "Python", "运行 Python 代码，支持后台任务和本地文件分析。", "执行", false},
	{toolScopeSession, "会话 Agent", "Read", "Read", "读取工作区内的文本文件。", "文件", false},
	{toolScopeSession, "会话 Agent", "Write", "Write", "创建或覆盖文件；工作区模式下限制在工作区内。", "文件", false},
	{toolScopeSession, "会话 Agent", "Edit", "Edit", "精确替换文件中的文本。", "文件", false},
	{toolScopeSession, "会话 Agent", "MultiEdit", "MultiEdit", "一次执行多组文本替换。", "文件", false},
	{toolScopeSession, "会话 Agent", "Glob", "Glob", "按文件名模式查找文件。", "文件", false},
	{toolScopeSession, "会话 Agent", "Grep", "Grep", "按正则搜索文件内容。", "文件", false},
	{toolScopeSession, "会话 Agent", "LS", "LS", "列出目录内容。", "文件", false},
	{toolScopeSession, "会话 Agent", "read_image", "read_image", "读取图片尺寸、格式，并可做 OCR。", "媒体", false},
	{toolScopeSession, "会话 Agent", "web_search", "web_search", "搜索互联网并返回结果列表。", "网络", false},
	{toolScopeSession, "会话 Agent", "web_fetch", "web_fetch", "抓取网页并转换成可读文本。", "网络", false},
	{toolScopeSession, "会话 Agent", "todo_write", "todo_write", "维护会话级待办列表。", "协作", false},
	{toolScopeSession, "会话 Agent", "job_list", "job_list", "列出当前会话的后台任务。", "任务", false},
	{toolScopeSession, "会话 Agent", "job_output", "job_output", "读取后台任务输出。", "任务", false},
	{toolScopeSession, "会话 Agent", "job_kill", "job_kill", "停止后台任务。", "任务", false},
	{toolScopeSession, "会话 Agent", "read_attachment", "read_attachment", "读取用户上传的附件内容。", "文件", false},

	// Task chat mainagent is intentionally read-only.
	{toolScopeTaskChat, "任务主 Agent", "get_work_trace", "get_work_trace", "读取一个 Work 的原始工具调用和结果。", "任务查询", false},
	{toolScopeTaskChat, "任务主 Agent", "search_task", "search_task", "搜索任务内的工具调用、发现和 LLM 输出。", "任务查询", false},
	{toolScopeTaskChat, "任务主 Agent", "get_llm_call", "get_llm_call", "读取一次 LLM 调用的完整记录。", "任务查询", false},
	{toolScopeTaskChat, "任务主 Agent", "read_attachment", "read_attachment", "读取任务附件内容。", "文件", false},

	// Planner and worker.
	{toolScopePlanner, "Planner", "propose_intents", "propose_intents", "向引擎提交下一批 Intent；planner 的核心输出。", "编排", true},
	{toolScopePlanner, "Planner", "propose_goal_met", "propose_goal_met", "声明当前 Goal 已完成。", "编排", true},
	{toolScopePlanner, "Planner", "list_quarantine", "list_quarantine", "列出失败、迟到或未确认结果的隔离摘要。", "编排", true},
	{toolScopePlanner, "Planner", "read_quarantine", "read_quarantine", "按 id 读取一条隔离结果的详细内容。", "编排", true},
	{toolScopeWorker, "Worker", "Read", "Read", "读取工作区文件。", "文件", false},
	{toolScopeWorker, "Worker", "Write", "Write", "创建或覆盖文件。", "文件", false},
	{toolScopeWorker, "Worker", "Edit", "Edit", "精确替换文本。", "文件", false},
	{toolScopeWorker, "Worker", "MultiEdit", "MultiEdit", "批量文本替换。", "文件", false},
	{toolScopeWorker, "Worker", "LS", "LS", "列出目录。", "文件", false},
	{toolScopeWorker, "Worker", "Glob", "Glob", "按文件名查找。", "文件", false},
	{toolScopeWorker, "Worker", "Grep", "Grep", "按内容搜索。", "文件", false},
	{toolScopeWorker, "Worker", "Bash", "Bash", "执行 shell 命令。", "执行", false},
	{toolScopeWorker, "Worker", "sleep", "sleep", "可取消的等待，配合后台任务使用。", "执行", false},
	{toolScopeWorker, "Worker", "HTTPRequest", "HTTPRequest", "发送原始 HTTP 请求并记录到流量。", "网络", false},
	{toolScopeWorker, "Worker", "WebFetch", "WebFetch", "抓取网页；仍受 worker 网络策略限制。", "网络", false},
	{toolScopeWorker, "Worker", "record_assets", "record_assets", "记录资产。", "产出", true},
	{toolScopeWorker, "Worker", "record_facts", "record_facts", "记录事实。", "产出", true},
	{toolScopeWorker, "Worker", "report_findings", "report_findings", "记录发现和证据。", "产出", true},
	{toolScopeWorker, "Worker", "attach_evidence", "attach_evidence", "附加脚本、截图或输出文件。", "产出", true},
	{toolScopeWorker, "Worker", "finish", "finish", "结束当前 Work 并提交结果。", "产出", true},
}

func toolScopeTitle(scope string) string {
	for _, t := range toolCatalog {
		if t.Scope == scope {
			return t.ScopeTitle
		}
	}
	return scope
}

func toolInfoByName(scope, name string) (toolInfo, bool) {
	for _, t := range toolCatalog {
		if t.Scope == scope && t.Name == name {
			return t, true
		}
	}
	return toolInfo{}, false
}
