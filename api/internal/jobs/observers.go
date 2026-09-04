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

// Resolvers chains payload resolvers, in order.
//
// The worker takes one, and by Phase 27 two features need one: backups, whose
// payload names a destination and must not carry its credentials in the queue,
// and deployments, whose payload is rebuilt from the deployment's own row.
//
// A resolver returns nil for a job type it does not own, which is the same
// convention a single resolver already followed — so chaining them is the first
// non-nil answer, and a job nobody claims keeps the payload it was queued with.
type Resolvers []PayloadResolver

// ResolvePayload asks each resolver in turn and returns the first answer.
func (r Resolvers) ResolvePayload(ctx context.Context, job Job) (map[string]any, error) {
	for _, resolver := range r {
		if resolver == nil {
			continue
		}
		payload, err := resolver.ResolvePayload(ctx, job)
		if err != nil {
			// Not swallowed, unlike an observer's failure. A payload that
			// cannot be completed is a job that must not be attempted: this is
			// the difference between a backup sent to a destination with no
			// credentials and one that is not sent at all.
			return nil, err
		}
		if payload != nil {
			return payload, nil
		}
	}
	return nil, nil
}
