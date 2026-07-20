// Package runner orchestrates mass domain checks: a concurrent work queue
// with retry-to-end semantics, per-source freezes, streaming per-domain
// results, graceful shutdown and resume integration.
package runner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"qda/internal/config"
	"qda/internal/domainx"
	"qda/internal/output"
	"qda/internal/ratelimit"
	"qda/internal/sources"
	"qda/internal/store"
	"qda/internal/types"
)

// Engine performs checks for single domains, applying cache reuse,
// per-source pacing/freezes and the fallback chain.
type Engine struct {
	settings config.Settings
	rdap     *sources.RDAPSource
	chain    *sources.Chain
	limiter  *ratelimit.Controller
	store    *store.Store
	printer  *output.Printer
}

// NewEngine wires the RDAP source, fallback chain and rate controller.
func NewEngine(settings config.Settings, db *store.Store, printer *output.Printer) (*Engine, error) {
	rdap, err := sources.NewRDAP(settings)
	if err != nil {
		return nil, err
	}
	return &Engine{
		settings: settings,
		rdap:     rdap,
		chain:    sources.NewChain(settings),
		limiter:  ratelimit.NewController(),
		store:    db,
		printer:  printer,
	}, nil
}

// Prepare loads the RDAP bootstrap.
func (e *Engine) Prepare(ctx context.Context) error {
	if err := e.rdap.Load(ctx); err != nil {
		return err
	}
	if e.rdap.BootstrapWarning != "" {
		e.printer.Warnf("%s", e.rdap.BootstrapWarning)
	}
	return nil
}

// FallbackEmpty reports whether registrar fallback was requested but no
// fallback source has credentials.
func (e *Engine) FallbackEmpty() bool {
	return !e.settings.RDAPOnly && e.chain.Empty()
}

// CheckDomain checks one domain synchronously with queue-style retries.
// Used by the API server.
func (e *Engine) CheckDomain(ctx context.Context, domain string) types.Result {
	target := types.Target{Domain: domain, Input: domain}
	var result types.Result
	for attempt := 1; attempt <= e.settings.MaxAttempts; attempt++ {
		res, retry := e.CheckOne(ctx, target, attempt)
		result = res
		if !retry || ctx.Err() != nil {
			return result
		}
		e.printer.Debugf("retrying %s after transient failure (attempt %d/%d)", domain, attempt, e.settings.MaxAttempts)
	}
	return result
}

// CheckOne checks one target once. The returned boolean tells the caller
// to requeue the target (moving it to the end of the queue) instead of
// emitting the result: rate limits freeze the source and transient network
// failures get another chance.
func (e *Engine) CheckOne(ctx context.Context, target types.Target, attempt int) (types.Result, bool) {
	started := time.Now()

	if !e.settings.ForceRecheck {
		if record, ok := e.store.Reusable(target.Domain, time.Now().UTC()); ok {
			result := store.ToResult(record, e.settings.ExpiringSoonDays, time.Now().UTC())
			result.Input = target.Input
			result.LineNumber = target.LineNumber
			return result, false
		}
	}

	rdapKey := e.rdap.KeyFor(target.Domain)
	e.limiter.SetInterval(rdapKey, e.rdapInterval())
	if err := e.limiter.Wait(ctx, rdapKey); err != nil {
		return types.Result{
			Domain:       target.Domain,
			Input:        target.Input,
			LineNumber:   target.LineNumber,
			Availability: types.AvailabilityUnknown,
			Lifecycle:    "unknown",
			Error:        err.Error(),
			CheckedAt:    time.Now().UTC(),
		}, false
	}

	rdapResult := e.rdap.Check(ctx, target)
	e.printer.Debugf("rdap %s -> %s (%s)", target.Domain, rdapResult.Availability, rdapResult.Lifecycle)

	if rdapResult.Availability == types.AvailabilityRateLimited {
		freeze := e.freezeDuration(rdapResult)
		e.limiter.Freeze(rdapKey, freeze)
		e.printer.Verbosef("rate limited by %s; freezing %s for %s (%s)", rdapKey, rdapKey, freeze, target.Domain)
		if attempt < e.settings.MaxAttempts {
			return rdapResult, true
		}
		// Attempts exhausted: try the registrar chain once before giving up.
		if result, outcome := e.tryChain(ctx, target, rdapResult); outcome != chainNone {
			return e.finish(result, started), false
		}
		return e.finish(rdapResult, started), false
	}

	if rdapResult.Retryable {
		e.printer.Verbosef("transient failure for %s: %s", target.Domain, rdapResult.Error)
		if attempt < e.settings.MaxAttempts {
			return rdapResult, true
		}
	}

	if e.finalizeFromRDAP(target, rdapResult) {
		result := rdapResult
		result.Sources = []types.SourceResult{types.SourceFromResult("rdap", rdapResult)}
		return e.finish(result, started), false
	}

	result, outcome := e.tryChain(ctx, target, rdapResult)
	switch outcome {
	case chainEmit:
		return e.finish(result, started), false
	case chainRetry:
		if attempt < e.settings.MaxAttempts {
			return result, true
		}
		return e.finish(result, started), false
	}

	result = rdapResult
	result.Sources = []types.SourceResult{types.SourceFromResult("rdap", rdapResult)}
	return e.finish(result, started), false
}

// chainOutcome tells CheckOne what to do after the fallback chain ran.
type chainOutcome int

const (
	// chainNone: no source could decide; emit the RDAP result.
	chainNone chainOutcome = iota
	// chainEmit: a source produced the final result.
	chainEmit
	// chainRetry: every source was rate limited or failed transiently and
	// froze; the item should move to the end of the queue.
	chainRetry
)

// tryChain walks the registrar fallback chain.
func (e *Engine) tryChain(ctx context.Context, target types.Target, rdapResult types.Result) (types.Result, chainOutcome) {
	if e.settings.RDAPOnly {
		return types.Result{}, chainNone
	}
	if e.settings.BRRDAPOnly && domainx.IsBRDomain(target.Domain) {
		return types.Result{}, chainNone
	}
	if e.chain.Empty() {
		return types.Result{}, chainNone
	}

	rdapResult.Sources = []types.SourceResult{types.SourceFromResult("rdap", rdapResult)}
	var lastSourceResult types.SourceResult
	var lastUnknown *types.Result
	rateLimitedSources := 0
	sourcesList := e.chain.Sources()
	for _, source := range sourcesList {
		key := "source:" + source.Name()
		e.limiter.SetInterval(key, e.sourceInterval(source.Name()))
		if err := e.limiter.Wait(ctx, key); err != nil {
			return types.Result{}, chainNone
		}

		sourceResult := source.Check(ctx, target.Domain)
		e.printer.Debugf("%s %s -> %s (%s) %s", source.Name(), target.Domain, sourceResult.Availability, sourceResult.Lifecycle, sourceResult.Error)

		if sourceResult.Availability == types.AvailabilityRateLimited {
			freeze := sourceResult.RetryAfter
			if !sourceResult.RetryAfterSet || freeze <= 0 {
				freeze = e.settings.SourceFreeze
			}
			freeze = clampFreeze(freeze, e.settings.SourceMaxDelay)
			e.limiter.Freeze(key, freeze)
			e.printer.Verbosef("%s rate limited; freezing source for %s (%s)", source.Name(), freeze, target.Domain)
			rateLimitedSources++
			lastSourceResult = sourceResult
			continue
		}
		if sourceResult.Retryable && sourceResult.Error != "" {
			// Network-level failure of this source: try the next one.
			lastSourceResult = sourceResult
			continue
		}

		result := sources.Compose(target, rdapResult, sourceResult, e.settings)
		if result.Availability == types.AvailabilityUnknown {
			// This source cannot decide (unsupported TLD, omission, API
			// failure): keep it as a fallback answer and try the next source.
			fallback := result
			lastUnknown = &fallback
			continue
		}
		return result, chainEmit
	}

	// Every source was rate limited: sources are frozen, requeue the item.
	if rateLimitedSources == len(sourcesList) && rateLimitedSources > 0 {
		return sources.ComposeRateLimited(target, rdapResult, lastSourceResult), chainRetry
	}
	// No source could decide: emit the best unknown answer we have.
	if lastUnknown != nil {
		return *lastUnknown, chainEmit
	}
	// Every source failed transiently: requeue the item; on the last
	// attempt the composed failure result is emitted instead.
	if lastSourceResult.Name != "" {
		result := sources.Compose(target, rdapResult, lastSourceResult, e.settings)
		return result, chainRetry
	}
	return types.Result{}, chainNone
}

// finalizeFromRDAP mirrors the old finalization rules.
func (e *Engine) finalizeFromRDAP(target types.Target, result types.Result) bool {
	if e.settings.RDAPOnly {
		return true
	}
	if e.settings.BRRDAPOnly && domainx.IsBRDomain(target.Domain) {
		return true
	}
	switch result.Availability {
	case types.AvailabilityRegistered, types.AvailabilityRedemption, types.AvailabilityPendingDelete:
		return true
	default:
		return false
	}
}

func (e *Engine) freezeDuration(result types.Result) time.Duration {
	if result.Lifecycle == "permission_denied" {
		return clampFreeze(e.settings.SourceFreeze, e.settings.SourceMaxDelay)
	}
	if result.RetryAfterSet && result.RetryAfter > 0 {
		return clampFreeze(result.RetryAfter, e.settings.SourceMaxDelay)
	}
	return clampFreeze(e.settings.SourceFreeze, e.settings.SourceMaxDelay)
}

func clampFreeze(value time.Duration, max time.Duration) time.Duration {
	if value < 0 {
		return 0
	}
	if max > 0 && value > max {
		return max
	}
	return value
}

func (e *Engine) rdapInterval() time.Duration {
	if e.settings.RDAP.RateLimit > 0 {
		return e.settings.RDAP.RateLimit
	}
	return e.settings.RateLimit
}

func (e *Engine) sourceInterval(name string) time.Duration {
	switch name {
	case "cloudflare":
		if e.settings.Cloudflare.RateLimit > 0 {
			return e.settings.Cloudflare.RateLimit
		}
	case "vercel":
		if e.settings.Vercel.RateLimit > 0 {
			return e.settings.Vercel.RateLimit
		}
	case "hostinger":
		if e.settings.Hostinger.RateLimit > 0 {
			return e.settings.Hostinger.RateLimit
		}
	}
	return e.settings.RateLimit
}

func (e *Engine) finish(result types.Result, started time.Time) types.Result {
	result.DurationMillis = time.Since(started).Milliseconds()
	return result
}

// ErrInterrupted marks a scan stopped by the user (SIGINT/SIGTERM).
var ErrInterrupted = errors.New("scan interrupted")

// ErrNoRDAPBootstrap marks a fatal bootstrap failure.
var ErrNoRDAPBootstrap = errors.New("rdap bootstrap unavailable")

// ValidateReadiness ensures the engine can actually run.
func (e *Engine) ValidateReadiness() error {
	if e.settings.RDAPOnly {
		return nil
	}
	if e.chain.Empty() {
		return fmt.Errorf("registrar fallback is enabled (rdap_only=false) but no fallback source has credentials; configure [cloudflare], [vercel] or [hostinger] in qda.toml or set rdap_only=true")
	}
	return nil
}
