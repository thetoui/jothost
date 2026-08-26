package jobs

import "context"

// Observers fans a finished job out to several reconcilers.
//
// One worker serves every kind of job, but the resources they touch are owned
// by different packages: websites reconciles a site's status, php reconciles a
// version's. Each is told about every job and ignores the ones that are not
// its own, which keeps the worker from having to know either.
type Observers []Observer

// JobFinished notifies each observer in turn.
//
// A panicking observer would otherwise take the worker's goroutine down with
// it and stop every subsequent job, so each call is isolated: reconciliation
// is bookkeeping, and failing at it must not stop work from being processed.
func (o Observers) JobFinished(ctx context.Context, job Job, state State,
	result map[string]any, failure string,
) {
	for _, observer := range o {
		if observer == nil {
			continue
		}
		notify(ctx, observer, job, state, result, failure)
	}
}

func notify(ctx context.Context, observer Observer, job Job, state State,
	result map[string]any, failure string,
) {
	defer func() {
		// Deliberately swallowed: the job's own outcome is already recorded,
		// and the alternative is one bad reconciler halting the queue.
		_ = recover()
	}()
	observer.JobFinished(ctx, job, state, result, failure)
}
