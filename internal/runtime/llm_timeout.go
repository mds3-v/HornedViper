package runtime

import (
	"context"
	"iter"
	"time"

	"github.com/Autumn-27/norma/llm"
)

// timeoutProvider bounds one model call. Session MaxDuration only protects the
// turn loop; without this a single blocked HTTP read can outlive it by tens of
// minutes.
type timeoutProvider struct {
	inner   llm.Provider
	timeout time.Duration
}

func (p *timeoutProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		ctx, cancel := context.WithTimeout(ctx, p.timeout)
		defer cancel()
		for ev, err := range p.inner.Stream(ctx, req) {
			if !yield(ev, err) {
				return
			}
		}
	}
}

func (p *timeoutProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	return p.inner.Complete(ctx, req)
}
