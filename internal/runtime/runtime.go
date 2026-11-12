package runtime

import (
	"context"

	"hornedviper/internal/domain"
)

// Runtime is the LLM/agent execution boundary.
//
// Phase 0 ships a deterministic FakeRuntime. The Norma adapter will implement
// the same interface with agentcore.Session per role.
type Runtime interface {
	Plan(ctx context.Context, req *domain.PlannerRequest) (*domain.PlannerResponse, error)
	Work(ctx context.Context, req *domain.WorkerRequest) (*domain.WorkResult, error)
}

// ConflictRuntime optionally reviews contradictory findings/flags.
type ConflictRuntime interface {
	ReviewConflicts(ctx context.Context, req *domain.ConflictReviewRequest) (*domain.ConflictReviewResponse, error)
}

// LeadRuntime optionally generates concrete lane plans for a swarm intent.
type LeadRuntime interface {
	Lead(ctx context.Context, req *domain.LeadRequest) (*domain.LeadResponse, error)
}
