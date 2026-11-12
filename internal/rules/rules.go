package rules

import (
	"regexp"
	"strings"

	"hornedviper/internal/domain"
)

// Engine evaluates enabled intercept rules in priority order.
type Engine struct {
	rules []compiledRule
}

type compiledRule struct {
	rule domain.InterceptRule
	re   *regexp.Regexp
}

func New(list []domain.InterceptRule) *Engine {
	e := &Engine{}
	for _, r := range list {
		if !r.Enabled || r.Target != "input" {
			continue
		}
		cr := compiledRule{rule: r}
		if r.MatchType == "regex" {
			if re, err := regexp.Compile(r.Pattern); err == nil {
				cr.re = re
			} else {
				continue
			}
		}
		e.rules = append(e.rules, cr)
	}
	return e
}

// Check returns the first matching rule's mode and name, or ("allow", "").
func (e *Engine) Check(tool string, input []byte) (string, string) {
	if e == nil {
		return "allow", ""
	}
	text := tool + "\n" + string(input)
	for _, cr := range e.rules {
		switch cr.rule.MatchType {
		case "regex":
			if cr.re != nil && cr.re.MatchString(text) {
				return cr.rule.Mode, cr.rule.Name
			}
		default:
			if strings.Contains(strings.ToLower(text), strings.ToLower(cr.rule.Pattern)) {
				return cr.rule.Mode, cr.rule.Name
			}
		}
	}
	return "allow", ""
}
