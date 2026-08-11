package local

import (
	"context"

	corelt "github.com/BogdanDolia/pod-rightsizer/pkg/loadtest"
	providerlt "github.com/BogdanDolia/pod-rightsizer/pkg/providers/loadtest"
)

// Tester wraps the existing core load test implementation to satisfy the provider interface.
type Tester struct{}

// New returns a new local load tester provider.
func New() *Tester { return &Tester{} }

func (t *Tester) Run(ctx context.Context, spec providerlt.RunSpec) error {
	rps := spec.RPS
	conc := spec.Concurrency
	lt := corelt.NewTester(spec.TargetURL, rps, conc)
	_, err := lt.Run(ctx, spec.Duration)
	return err
}
