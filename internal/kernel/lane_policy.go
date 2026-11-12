package kernel

import "hornedviper/internal/domain"

// LanePolicy is the explicit task-profile boundary for parallel lane semantics.
// It deliberately lives outside the generic scheduler and merger so CTF rules
// cannot silently become the rules for every task type.
type LanePolicy interface {
	Name() string
	FlagQuorumEnabled() bool
	SubmitFlags() bool
}

type ctfLanePolicy struct{}

func (ctfLanePolicy) Name() string            { return "ctf" }
func (ctfLanePolicy) FlagQuorumEnabled() bool { return true }
func (ctfLanePolicy) SubmitFlags() bool       { return true }

type genericLanePolicy struct{}

func (genericLanePolicy) Name() string            { return "generic" }
func (genericLanePolicy) FlagQuorumEnabled() bool { return false }
func (genericLanePolicy) SubmitFlags() bool       { return false }

func lanePolicyForTask(task *domain.Task) LanePolicy {
	if task != nil && task.Mode == domain.ModeCTF {
		return ctfLanePolicy{}
	}
	return genericLanePolicy{}
}
