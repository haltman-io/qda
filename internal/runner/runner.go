package runner

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"qda/internal/config"
	"qda/internal/output"
	"qda/internal/resume"
	"qda/internal/store"
	"qda/internal/types"
)

// Stats tracks live scan counters.
type Stats struct {
	Checked   atomic.Int64
	Available atomic.Int64
	Soon      atomic.Int64
	RateLimit atomic.Int64
	Requeued  atomic.Int64
}

// Runner executes a mass scan.
type Runner struct {
	engine  *Engine
	printer *output.Printer
	store   *store.Store
	resume  *resume.Manager
	stats   Stats
	// OnResult is called for every emitted result (used for notifications).
	OnResult func(types.Result)
	// JSONL receives results when JSON-lines mode is active.
	JSONL *output.JSONLWriter
	// HideRegistered filters REGISTERED/RESERVED lines from the console.
	HideRegistered bool
}

// NewRunner creates a Runner.
func NewRunner(engine *Engine, db *store.Store, printer *output.Printer, resumeManager *resume.Manager) *Runner {
	return &Runner{
		engine:  engine,
		printer: printer,
		store:   db,
		resume:  resumeManager,
	}
}

// Stats returns the live counters.
func (r *Runner) Stats() *Stats { return &r.stats }

// Run executes the scan and returns every result (including cache hits).
func (r *Runner) Run(ctx context.Context, targets []types.Target) ([]types.Result, error) {
	total := len(targets)
	if total == 0 {
		return nil, nil
	}

	queue := newWorkQueue()
	results := make(chan types.Result, r.engine.settings.Concurrency*2)

	var collectMu sync.Mutex
	collected := make([]types.Result, 0, total)
	var collectWG sync.WaitGroup
	collectWG.Add(1)
	go func() {
		defer collectWG.Done()
		for result := range results {
			collectMu.Lock()
			collected = append(collected, result)
			collectMu.Unlock()

			r.stats.Checked.Add(1)
			if types.IsAvailableLike(result) {
				r.stats.Available.Add(1)
			} else if types.IsAvailableSoon(result) {
				r.stats.Soon.Add(1)
			}
			if result.Availability == types.AvailabilityRateLimited {
				r.stats.RateLimit.Add(1)
			}

			if !result.CacheHit {
				r.store.Put(result)
			}
			if r.resume != nil {
				r.resume.Complete(result.Domain)
			}
			if r.JSONL != nil {
				_ = r.JSONL.Write(result)
			} else {
				r.printer.Result(result, r.HideRegistered)
			}
			if r.OnResult != nil {
				r.OnResult(result)
			}
		}
	}()

	// Workers.
	var workerWG sync.WaitGroup
	concurrency := r.engine.settings.Concurrency
	for i := 0; i < concurrency; i++ {
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			for {
				item, ok := queue.Pop(ctx)
				if !ok {
					return
				}
				result, retry := r.engine.CheckOne(ctx, item.target, item.attempts+1)
				if ctx.Err() != nil {
					queue.Done(item, true)
					return
				}
				if retry && item.attempts+1 < r.engine.settings.MaxAttempts {
					r.stats.Requeued.Add(1)
					queue.Done(item, true)
					continue
				}
				queue.Done(item, false)
				select {
				case results <- result:
				case <-ctx.Done():
					collectMu.Lock()
					collected = append(collected, result)
					collectMu.Unlock()
					return
				}
			}
		}()
	}

	for _, target := range targets {
		queue.PushBack(queueItem{target: target})
	}
	queue.Close()

	// Progress reporter (optional — results and other [INF] logs still stream).
	progressDone := make(chan struct{})
	if r.engine.settings.ShowProgress {
		go func() {
			ticker := time.NewTicker(r.engine.settings.ProgressInterval)
			defer ticker.Stop()
			started := time.Now()
			for {
				select {
				case <-progressDone:
					return
				case <-ticker.C:
					checked := r.stats.Checked.Load()
					if checked >= int64(total) {
						continue
					}
					elapsed := time.Since(started)
					rate := 0.0
					if elapsed > 0 {
						rate = float64(checked) / elapsed.Seconds()
					}
					eta := "n/a"
					if rate > 0 {
						eta = (time.Duration(float64(total-int(checked))/rate) * time.Second).Round(time.Second).String()
					}
					r.printer.Infof("progress %d/%d (%.1f%%) | %.1f checks/s | available %d | soon %d | requeued %d | eta %s",
						checked, total, float64(checked)/float64(total)*100, rate,
						r.stats.Available.Load(), r.stats.Soon.Load(), r.stats.Requeued.Load(), eta)
				}
			}
		}()
	}

	workerWG.Wait()
	if r.engine.settings.ShowProgress {
		close(progressDone)
	}
	close(results)
	collectWG.Wait()

	if ctx.Err() != nil {
		if r.resume != nil {
			r.resume.SetPending(queue.Snapshot())
			if err := r.resume.Save(); err != nil {
				r.printer.Warnf("could not save resume state: %v", err)
			} else {
				r.printer.Infof("scan state saved; resume later with: qda run -resume")
			}
		}
		return collected, ErrInterrupted
	}
	return collected, nil
}

// SaveStore persists the store when dirty.
func SaveStore(db *store.Store, printer *output.Printer) {
	if db == nil || !db.Dirty() {
		return
	}
	if err := db.Save(); err != nil {
		printer.Warnf("could not save local database: %v", err)
	}
}

// Autosave periodically persists the store and resume state while dirty.
func Autosave(ctx context.Context, db *store.Store, resumeManager *resume.Manager, interval time.Duration, printer *output.Printer) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				SaveStore(db, printer)
				if resumeManager != nil {
					if err := resumeManager.Save(); err != nil {
						printer.Debugf("could not autosave resume state: %v", err)
					}
				}
			}
		}
	}()
}

// PrintBannerlessStart prints the standard scan preamble.
func PrintStartInfo(printer *output.Printer, settings config.Settings, total int, resumed bool) {
	printer.Infof("loaded %d targets | concurrency %d | rate limit %s | tlds %v",
		total, settings.Concurrency, settings.RateLimit, settings.TLDs)
	if resumed {
		printer.Infof("resuming previous scan state")
	}
	if settings.ForceRecheck {
		printer.Infof("force recheck enabled; local database reuse is disabled")
	}
	if len(settings.Proxies) > 0 || settings.ProxyFile != "" {
		printer.Infof("proxy rotation enabled")
	}
	if !settings.RDAPOnly {
		printer.Infof("registrar fallback enabled (cloudflare/vercel/hostinger)")
	}
}

// Errf formats scan-level fatal errors with context.
func Errf(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}
