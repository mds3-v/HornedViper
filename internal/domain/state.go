package domain

import (
	"fmt"
	"strings"
)

// CanTransition reports whether a task status change is legal.
func (s TaskStatus) CanTransition(to TaskStatus) bool {
	switch s {
	case TaskCreated:
		return to == TaskPlanning || to == TaskCancelled || to == TaskFailed
	case TaskPlanning:
		return to == TaskRunning || to == TaskPaused || to == TaskCancelled || to == TaskFailed
	case TaskRunning:
		return to == TaskSettling || to == TaskPaused || to == TaskCancelled || to == TaskFailed
	case TaskSettling:
		return to == TaskCompleted || to == TaskFailed || to == TaskCancelled
	case TaskPaused:
		return to == TaskRunning || to == TaskCancelled
	}
	return false
}

// CanTransition reports whether a goal state change is legal.
func (s GoalState) CanTransition(to GoalState) bool {
	switch s {
	case GoalOpen:
		return to == GoalInProgress || to == GoalPaused || to == GoalMet || to == GoalAbandoned
	case GoalInProgress:
		return to == GoalPaused || to == GoalMet || to == GoalAbandoned
	case GoalPaused:
		return to == GoalInProgress || to == GoalMet || to == GoalAbandoned
	}
	return false
}

// CanTransition reports whether an intent state change is legal.
func (s IntentState) CanTransition(to IntentState) bool {
	switch s {
	case IntentProposed:
		return to == IntentApproved || to == IntentCancelled
	case IntentApproved:
		return to == IntentQueued || to == IntentDeferred || to == IntentCancelled
	case IntentQueued:
		return to == IntentClaimed || to == IntentDeferred || to == IntentCancelled
	case IntentDeferred:
		return to == IntentApproved || to == IntentQueued || to == IntentCancelled
	case IntentClaimed:
		return to == IntentRunning || to == IntentFailed || to == IntentCancelled
	case IntentRunning:
		return to == IntentSucceeded || to == IntentFailed || to == IntentBlocked || to == IntentCancelled
	}
	return false
}

// CanTransition reports whether a work state change is legal.
func (s WorkState) CanTransition(to WorkState) bool {
	switch s {
	case WorkQueued:
		return to == WorkRunning || to == WorkCancelled
	case WorkLeased:
		return to == WorkRunning || to == WorkLost || to == WorkCancelled
	case WorkRunning:
		return to == WorkSettling || to == WorkFailed || to == WorkTimedOut || to == WorkCancelled
	case WorkSettling:
		return to == WorkSucceeded || to == WorkFailed || to == WorkTimedOut
	}
	return false
}

// CanTransition reports whether an approval decision change is legal.
func (s ApprovalDecision) CanTransition(to ApprovalDecision) bool {
	return s == ApprovalPending && (to == ApprovalApproved || to == ApprovalRejected || to == ApprovalExpired)
}

var allowedDangerousActions = map[string]bool{
	"delete":            true,
	"modify":            true,
	"credential_access": true,
}

var allowedSeverity = map[string]bool{
	"info": true, "low": true, "medium": true, "high": true, "critical": true,
}

// ValidateIntentSpec checks the planner's intent before persistence.
func ValidateIntentSpec(spec IntentSpec) error {
	if strings.TrimSpace(spec.Summary) == "" {
		return fmt.Errorf("intent.summary is required")
	}
	if spec.Priority < 0 || spec.Priority > 10 {
		return fmt.Errorf("intent.priority must be 0..10")
	}
	switch spec.Risk {
	case RiskLow, RiskMedium, RiskHigh, RiskCritical:
	default:
		return fmt.Errorf("intent.risk %q is invalid", spec.Risk)
	}
	switch spec.ExecutionMode {
	case ExecutionSingle, ExecutionShard, ExecutionSwarm:
	default:
		return fmt.Errorf("intent.execution_mode %q is invalid", spec.ExecutionMode)
	}
	if spec.ExpectedFlags < 0 {
		return fmt.Errorf("intent.expected_flags must be >= 0")
	}
	for _, action := range spec.DangerousActions {
		if !allowedDangerousActions[action] {
			return fmt.Errorf("intent.dangerous_action %q is not allowed", action)
		}
	}
	return nil
}

// ValidateWorkResult checks the worker's four-category output.
func ValidateWorkResult(result WorkResult) error {
	switch result.Status {
	case "done", "blocked", "failed":
	default:
		return fmt.Errorf("work.status %q is invalid", result.Status)
	}
	for i, f := range result.Facts {
		if strings.TrimSpace(f.Summary) == "" {
			return fmt.Errorf("facts[%d].summary is required", i)
		}
		if strings.TrimSpace(f.Evidence) == "" {
			return fmt.Errorf("facts[%d].evidence is required", i)
		}
		if f.Confidence != ConfidenceObserved && f.Confidence != ConfidenceInferred {
			return fmt.Errorf("facts[%d].confidence must be observed or inferred", i)
		}
	}
	for i, f := range result.Findings {
		if strings.TrimSpace(f.Title) == "" {
			return fmt.Errorf("findings[%d].title is required", i)
		}
		if strings.TrimSpace(f.Evidence) == "" {
			return fmt.Errorf("findings[%d].evidence is required", i)
		}
		if !allowedSeverity[f.Severity] {
			return fmt.Errorf("findings[%d].severity %q is invalid", i, f.Severity)
		}
	}
	for i, a := range result.Assets {
		if strings.TrimSpace(a.Type) == "" || strings.TrimSpace(a.Value) == "" {
			return fmt.Errorf("assets[%d] requires type and value", i)
		}
	}
	return nil
}

// IsDangerousIntent reports whether a pentest intent needs a human.
func IsDangerousIntent(intent *Intent) bool {
	if intent == nil {
		return false
	}
	if intent.Risk == RiskHigh || intent.Risk == RiskCritical {
		return true
	}
	return len(intent.DangerousActions) > 0
}
