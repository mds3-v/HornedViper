package domain

import "testing"

func TestTaskTransitions(t *testing.T) {
	ok := [][2]TaskStatus{
		{TaskCreated, TaskPlanning},
		{TaskPlanning, TaskRunning},
		{TaskRunning, TaskSettling},
		{TaskSettling, TaskCompleted},
		{TaskRunning, TaskPaused},
		{TaskPaused, TaskRunning},
	}
	for _, p := range ok {
		if !p[0].CanTransition(p[1]) {
			t.Errorf("%s -> %s should be allowed", p[0], p[1])
		}
	}
	bad := [][2]TaskStatus{
		{TaskCompleted, TaskRunning},
		{TaskCreated, TaskCompleted},
		{TaskSettling, TaskPaused},
	}
	for _, p := range bad {
		if p[0].CanTransition(p[1]) {
			t.Errorf("%s -> %s should be denied", p[0], p[1])
		}
	}
}

func TestIntentTransitions(t *testing.T) {
	if !IntentProposed.CanTransition(IntentApproved) {
		t.Fatal("proposed -> approved should be allowed")
	}
	if !IntentApproved.CanTransition(IntentQueued) {
		t.Fatal("approved -> queued should be allowed")
	}
	if IntentSucceeded.CanTransition(IntentRunning) {
		t.Fatal("succeeded -> running should be denied")
	}
}

func TestValidateIntentSpec(t *testing.T) {
	base := IntentSpec{Summary: "recon", Priority: 5, Risk: RiskLow, ExecutionMode: ExecutionSingle}
	if err := ValidateIntentSpec(base); err != nil {
		t.Fatalf("base spec: %v", err)
	}
	badAction := base
	badAction.DangerousActions = []string{"format_disk"}
	if err := ValidateIntentSpec(badAction); err == nil {
		t.Fatal("unknown dangerous action should fail")
	}
	badRisk := base
	badRisk.Risk = "unknown"
	if err := ValidateIntentSpec(badRisk); err == nil {
		t.Fatal("unknown risk should fail")
	}
}

func TestValidateWorkResult(t *testing.T) {
	ok := WorkResult{
		Status: "done",
		Facts: []FactSpec{{
			Summary: "reachable", Evidence: "curl -> 200", Confidence: ConfidenceObserved,
		}},
		Findings: []FindingSpec{{
			Title: "demo", Severity: "high", Evidence: "flag{demo}",
		}},
	}
	if err := ValidateWorkResult(ok); err != nil {
		t.Fatalf("valid result: %v", err)
	}
	bad := ok
	bad.Findings[0].Severity = "urgent"
	if err := ValidateWorkResult(bad); err == nil {
		t.Fatal("invalid severity should fail")
	}
	missingEvidence := ok
	missingEvidence.Facts[0].Evidence = ""
	if err := ValidateWorkResult(missingEvidence); err == nil {
		t.Fatal("missing evidence should fail")
	}
}

func TestIsDangerousIntent(t *testing.T) {
	if IsDangerousIntent(&Intent{Risk: RiskHigh}) != true {
		t.Fatal("high risk should be dangerous")
	}
	if IsDangerousIntent(&Intent{Risk: RiskLow, DangerousActions: []string{"modify"}}) != true {
		t.Fatal("modify should be dangerous")
	}
	if IsDangerousIntent(&Intent{Risk: RiskMedium}) != false {
		t.Fatal("medium risk without dangerous action should be auto")
	}
}
