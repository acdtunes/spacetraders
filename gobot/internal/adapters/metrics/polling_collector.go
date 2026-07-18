package metrics

import (
	"context"
	"sync"
	"time"
)

// pollingCollector is the shared lifecycle scaffolding for the metrics
// collectors that refresh their gauges on a fixed interval (container,
// financial, manufacturing, market). It owns the cancellable context and the
// WaitGroup so each embedding collector supplies only its poll callback(s) and
// interval(s) via startPolling — keeping ticker/goroutine construction and the
// graceful-shutdown policy in a single place.
type pollingCollector struct {
	ctx        context.Context
	cancelFunc context.CancelFunc
	wg         sync.WaitGroup
}

// startContext derives the cancellable context that bounds every poll loop
// launched via startPolling. Call once from the embedding collector's Start,
// before any startPolling call.
func (p *pollingCollector) startContext(ctx context.Context) {
	p.ctx, p.cancelFunc = context.WithCancel(ctx)
}

// startPolling launches one ticker-driven poll loop in its own goroutine. When
// pollImmediately is true the callback runs once synchronously before the first
// tick (initial poll); the container/ship loops pass false to preserve their
// tick-only cadence. The loop exits when startContext's context is cancelled.
func (p *pollingCollector) startPolling(interval time.Duration, pollImmediately bool, poll func()) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		if pollImmediately {
			poll()
		}

		for {
			select {
			case <-p.ctx.Done():
				return
			case <-ticker.C:
				poll()
			}
		}
	}()
}

// Stop cancels the poll context and blocks until every poll goroutine started
// via startPolling has drained.
func (p *pollingCollector) Stop() {
	if p.cancelFunc != nil {
		p.cancelFunc()
	}
	p.wg.Wait()
}
