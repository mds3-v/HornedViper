package api

import (
	"encoding/json"
	"fmt"

	"github.com/Autumn-27/norma/llm"

	"hornedviper/internal/domain"
	"hornedviper/internal/store"
)

type laneCallView struct {
	Call   domain.LLMCall
	Prompt string
	Usage  string
}

type laneView struct {
	Title        string
	Tab          string
	Task         *domain.Task
	Work         *domain.Work
	Intent       *domain.Intent
	Run          *store.SwarmRun
	Result       *store.SwarmLaneResult
	ResultJSON   string
	Calls        []laneCallView
	Activities   []domain.Activity
	Quarantine   []domain.QuarantineItem
	Artifacts    []domain.Evidence
	LaneStrategy string
	LaneBrief    string
}

func newLaneView(task *domain.Task, work *domain.Work, intent *domain.Intent, run *store.SwarmRun,
	result *store.SwarmLaneResult, calls []domain.LLMCall, activities []domain.Activity,
	quarantine []domain.QuarantineItem, artifacts []domain.Evidence) *laneView {
	v := &laneView{Title: "Lane", Tab: "tasks", Task: task, Work: work, Intent: intent, Run: run, Result: result,
		ResultJSON: "{}", Activities: activities, Quarantine: quarantine, Artifacts: artifacts}
	if result != nil {
		v.ResultJSON = result.ResultJSON
	}
	for _, c := range calls {
		var req struct {
			System   []string
			Messages []llm.Message
		}
		_ = json.Unmarshal([]byte(c.RequestJSON), &req)
		v.Calls = append(v.Calls, laneCallView{
			Call: c, Prompt: formatTracePrompt(req.System, req.Messages),
			Usage: fmt.Sprintf("%d / %d", usageFor(c).Input, usageFor(c).Output),
		})
	}
	if run != nil {
		for i := 0; i < run.Lanes; i++ {
			if laneIDForIndex(i) == work.LaneID {
				if i < len(run.Strategies) {
					v.LaneStrategy = run.Strategies[i]
				}
				if i < len(run.Briefs) {
					v.LaneBrief = run.Briefs[i]
				}
			}
		}
	}
	return v
}

func usageFor(c domain.LLMCall) domain.TokenUsage {
	var u domain.TokenUsage
	_ = json.Unmarshal([]byte(c.UsageJSON), &u)
	return u
}
