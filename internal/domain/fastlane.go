package domain

import "strings"

// IsFastIntent reports whether an Intent is a short follow-up whose only job
// is to submit, verify, finalize or tear down evidence that already exists.
// These intents must not queue behind long-running solve work.
func IsFastIntent(i *Intent) bool {
	if i == nil {
		return false
	}
	return isFastIntentFields(i.Summary, i.ExpectedOutputs, i.Budget)
}

// IsFastIntentSpec is the IntentSpec variant used by the store when scanning
// queued intents.
func IsFastIntentSpec(s IntentSpec) bool {
	return isFastIntentFields(s.Summary, s.ExpectedOutputs, s.Budget)
}

func isFastIntentFields(summary string, expected []string, budget map[string]any) bool {
	if budget != nil {
		if v, ok := budget["fast_lane"].(bool); ok {
			return v
		}
	}
	s := strings.ToLower(strings.TrimSpace(summary))
	for _, prefix := range []string{
		"submit ", "verify ", "finalize ", "teardown ",
		"resubmit ", "resolve the e3-01 scoring discrepancy",
	} {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	if strings.Contains(s, "submit the captured") ||
		strings.Contains(s, "scoring discrepancy") ||
		strings.Contains(s, "coverage reconciliation") {
		return true
	}
	// Avoid classifying a full solve/exploit brief merely because it mentions
	// submit or verify in its expected outputs.
	for _, prefix := range []string{"solve ", "start and solve ", "attack ", "recover ", "enumerate ", "craft "} {
		if strings.HasPrefix(s, prefix) {
			return false
		}
	}
	_ = expected
	return false
}
