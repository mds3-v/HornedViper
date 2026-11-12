package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Autumn-27/norma/permission"
)

var artifactTaskPattern = regexp.MustCompile(`(?i)artifacts[\\/]+task-([0-9]+)`)
var taskTokenPattern = regexp.MustCompile(`(?i)\btask-([0-9]+)\b`)

func (r *NormaRuntime) canUseToolFor(taskID int64, runDir string) func(context.Context, string, json.RawMessage, permission.Context) permission.Decision {
	return func(ctx context.Context, toolName string, input json.RawMessage, pctx permission.Context) permission.Decision {
		if reason := sandboxViolation(toolName, input, taskID, runDir); reason != "" {
			return permission.Denied(reason)
		}
		return r.canUseTool(ctx, toolName, input, pctx)
	}
}

func sandboxViolation(toolName string, input json.RawMessage, taskID int64, runDir string) string {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	text := ""
	for _, k := range []string{"command", "path", "file_path", "pattern", "url"} {
		if v, ok := in[k].(string); ok {
			text += " " + v
		}
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "hornedviper.db") {
		return "worker isolation: database access is not allowed"
	}
	for _, m := range artifactTaskPattern.FindAllStringSubmatch(lower, -1) {
		if len(m) > 1 && m[1] != itoa(taskID) {
			return "worker isolation: cross-task artifact access is not allowed"
		}
	}
	for _, m := range taskTokenPattern.FindAllStringSubmatch(lower, -1) {
		if len(m) > 1 && m[1] != itoa(taskID) {
			return "worker isolation: cross-task task directory is not allowed"
		}
	}
	// File tools must stay inside the current Work directory. /tmp is allowed
	// for normal tool output; artifact roots are not.
	if runDir != "" {
		for _, k := range []string{"path", "file_path"} {
			if v, ok := in[k].(string); ok && strings.TrimSpace(v) != "" {
				p := v
				if !filepath.IsAbs(p) {
					p = filepath.Join(runDir, p)
				}
				abs, err := filepath.Abs(p)
				if err != nil {
					continue
				}
				root, _ := filepath.Abs(runDir)
				tmp, _ := filepath.Abs("/tmp")
				if !strings.HasPrefix(abs, root+string(filepath.Separator)) && !strings.HasPrefix(abs, tmp+string(filepath.Separator)) {
					return "worker isolation: path is outside the task work directory"
				}
			}
		}
	}
	// Bash can still cd with an absolute path; reject the known workspace and
	// artifact roots rather than pretending shell text can be fully sandboxed.
	workspace := strings.ToLower("/Users/mds3/Documents/ctf-agent-vibecoding")
	ownArtifacts := workspace + "/artifacts/task-" + itoa(taskID)
	if strings.Contains(lower, workspace) && !strings.Contains(lower, ownArtifacts) {
		return "worker isolation: workspace root access is not allowed"
	}
	_ = toolName
	return ""
}
