package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"hornedviper/internal/domain"
)

// FakeRuntime is a deterministic Phase 0 runtime.
//
// It plays both planner and worker:
//   - no findings yet -> plan "recon"
//   - recon produced a fact -> plan "exploit"
//   - exploit produced a finding -> propose goal met
type FakeRuntime struct {
	StepDelay time.Duration
	// Risky makes the exploit intent high risk with a dangerous action, so a
	// pentest task stops at the approval gate instead of auto-approving.
	Risky bool
}

func (f *FakeRuntime) Plan(ctx context.Context, req *domain.PlannerRequest) (*domain.PlannerResponse, error) {
	if req == nil || req.Task == nil || req.Goal == nil || req.Packet == nil {
		return nil, fmt.Errorf("fake planner: incomplete request")
	}
	if len(req.Packet.NewFindings) > 0 {
		ids := make([]int64, 0, len(req.Packet.NewFindings))
		for _, finding := range req.Packet.NewFindings {
			ids = append(ids, finding.ID)
		}
		return &domain.PlannerResponse{
			GoalMet: &domain.GoalMetProposal{
				GoalID: req.Goal.ID, EvidenceIDs: ids,
				Reason: "demo finding confirms the goal",
			},
		}, nil
	}

	if len(req.Packet.OpenIntents) > 0 {
		return &domain.PlannerResponse{}, nil
	}
	if len(req.Packet.NewFacts) == 0 {
		return &domain.PlannerResponse{Intents: []domain.IntentSpec{{
			Summary:         "recon: enumerate the demo target",
			Rationale:       "no facts yet; start with read-only reconnaissance",
			GoalIDs:         []int64{req.Goal.ID},
			Priority:        5,
			Risk:            domain.RiskLow,
			ExecutionMode:   domain.ExecutionSingle,
			ExpectedOutputs: []string{"asset", "fact"},
		}}}, nil
	}

	risk := domain.RiskMedium
	var dangerous []string
	if f.Risky {
		risk = domain.RiskHigh
		dangerous = []string{"modify"}
	}
	return &domain.PlannerResponse{Intents: []domain.IntentSpec{{
		Summary:          "exploit: validate the demo lead",
		Rationale:        "recon found a reachable endpoint",
		GoalIDs:          []int64{req.Goal.ID},
		Priority:         7,
		Risk:             risk,
		DangerousActions: dangerous,
		ExecutionMode:    domain.ExecutionSingle,
		ExpectedOutputs:  []string{"finding", "evidence"},
	}}}, nil
}

func (f *FakeRuntime) Work(ctx context.Context, req *domain.WorkerRequest) (*domain.WorkResult, error) {
	if req == nil || req.Intent == nil || req.Work == nil {
		return nil, fmt.Errorf("fake worker: incomplete request")
	}
	if f.StepDelay > 0 {
		select {
		case <-time.After(f.StepDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	summary := strings.ToLower(req.Intent.Summary)
	switch {
	case strings.Contains(summary, "recon"):
		return &domain.WorkResult{
			Status: "done",
			Assets: []domain.AssetSpec{{
				Type: "endpoint", Value: "https://demo.local/",
				Metadata: map[string]any{"status": 200},
			}},
			Facts: []domain.FactSpec{{
				Summary:    "demo target is reachable",
				Detail:     "GET / returns 200 with a login form",
				Evidence:   "curl -s -o /dev/null -w '%{http_code}' https://demo.local/ -> 200",
				Confidence: domain.ConfidenceObserved,
			}},
			Evidence: []domain.EvidenceSpec{{
				Type: "file", Path: "artifacts/recon.txt",
			}},
		}, nil
	case strings.Contains(summary, "exploit"):
		return &domain.WorkResult{
			Status: "done",
			Facts: []domain.FactSpec{{
				Summary:    "demo flag recovered from the login response",
				Detail:     "the response includes a debug header",
				Evidence:   "curl -s https://demo.local/ | grep flag",
				Confidence: domain.ConfidenceObserved,
			}},
			Findings: []domain.FindingSpec{{
				Title: "demo flag", Severity: "high",
				Evidence: "flag{demo_hornedviper_phase0}",
			}},
			Evidence: []domain.EvidenceSpec{{
				Type: "file", Path: "artifacts/exploit.txt",
			}},
		}, nil
	default:
		return &domain.WorkResult{
			Status: "done",
			Facts: []domain.FactSpec{{
				Summary:    "intent executed",
				Detail:     req.Intent.Summary,
				Evidence:   "fake runtime",
				Confidence: domain.ConfidenceInferred,
			}},
		}, nil
	}
}
