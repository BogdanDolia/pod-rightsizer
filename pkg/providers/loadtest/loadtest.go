package loadtest

import (
	"context"
	"time"
)

// RunSpec describes parameters for a load test run.
type RunSpec struct {
	TargetURL   string        `json:"targetURL"`
	Duration    time.Duration `json:"duration"`
	RPS         int           `json:"rps,omitempty"`
	Concurrency int           `json:"concurrency,omitempty"`
}

// LoadTester abstracts running a load test.
type LoadTester interface {
	Run(ctx context.Context, spec RunSpec) error
}
