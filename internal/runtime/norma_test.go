package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/tool"

	"hornedviper/internal/domain"
)

func TestProposeIntentsToolCaptures(t *testing.T) {
	cap := &plannerCapture{}
	tl := proposeIntentsTool(cap)
	input := json.RawMessage(`{"intents":[{"summary":"recon","rationale":"start","priority":5,"risk":"low","execution_mode":"single"}]}`)
	res, err := tl.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %s", res.Flatten())
	}
	if !cap.Called || len(cap.Intents) != 1 || cap.Intents[0].Summary != "recon" {
		t.Fatalf("capture = %+v", cap)
	}
}

func TestWorkerToolsCaptureFourCategories(t *testing.T) {
	cap := &workerCapture{}
	steps := []struct {
		tl    tool.CoreTool
		input string
	}{
		{recordAssetsTool(cap), `{"assets":[{"type":"endpoint","value":"https://demo.local/"}]}`},
		{recordFactsTool(cap), `{"facts":[{"summary":"reachable","evidence":"curl -> 200","confidence":"observed"}]}`},
		{reportFindingsTool(cap), `{"findings":[{"title":"flag","severity":"high","evidence":"flag{demo}"}]}`},
		{attachEvidenceTool(cap), `{"evidence":[{"type":"file","path":"solve.py"}]}`},
		{finishTool(cap), `{"status":"done"}`},
	}
	for _, step := range steps {
		if _, err := step.tl.Call(context.Background(), json.RawMessage(step.input), nil); err != nil {
			t.Fatalf("call: %v", err)
		}
	}
	if !cap.Finished || cap.Result.Status != "done" {
		t.Fatalf("finish not captured: %+v", cap)
	}
	if len(cap.Result.Assets) != 1 || len(cap.Result.Facts) != 1 || len(cap.Result.Findings) != 1 || len(cap.Result.Evidence) != 1 {
		t.Fatalf("result = %+v", cap.Result)
	}
}

func TestNormaRuntimeRequiresProvider(t *testing.T) {
	if _, err := NewNormaRuntime(NormaConfig{}); err == nil {
		t.Fatal("expected an error without provider or API key")
	}
}

func TestTraceBufferCoalescesStreamDeltas(t *testing.T) {
	var got []domain.Activity
	r := &NormaRuntime{trace: func(a domain.Activity) { got = append(got, a) }}
	buf := newTraceBuffer(r, 1, 2, 3, "worker")

	buf.add(harness.Event{Kind: harness.KindThinking, Text: "try "})
	buf.add(harness.Event{Kind: harness.KindThinking, Text: "the oracle"})
	buf.add(harness.Event{Kind: harness.KindText, Text: "running "})
	buf.add(harness.Event{Kind: harness.KindText, Text: "a tool"})
	buf.add(harness.Event{
		Kind: harness.KindToolUse,
		ToolUse: &llm.ContentBlock{
			Type: llm.BlockToolUse, Name: "bash", ID: "call-1",
			Input: json.RawMessage(`{"command":"ls"}`),
		},
	})
	buf.add(harness.Event{
		Kind: harness.KindToolResult,
		ToolResult: &llm.ContentBlock{
			Type: llm.BlockToolResult, ToolUseID: "call-1",
			Content: []llm.ContentBlock{llm.TextBlock("a.txt")},
		},
	})
	buf.flush()

	if len(got) != 4 {
		t.Fatalf("got %d activities: %+v", len(got), got)
	}
	if got[0].Kind != "thinking" || got[0].Detail != "try the oracle" {
		t.Fatalf("thinking activity = %+v", got[0])
	}
	if got[1].Kind != "assistant_text" || got[1].Detail != "running a tool" {
		t.Fatalf("assistant_text activity = %+v", got[1])
	}
	if got[2].Kind != "tool_use" || got[2].Tool != "bash" || got[2].ToolUseID != "call-1" {
		t.Fatalf("tool_use activity = %+v", got[2])
	}
	if got[3].Kind != "tool_result" {
		t.Fatalf("tool_result activity = %+v", got[3])
	}
}
