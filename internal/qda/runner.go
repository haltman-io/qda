package qda

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type RunnerOutput struct {
	Results []Result
	Skipped []SkippedInput
	Warning string
}

func RunChecks(ctx context.Context, settings Settings, targets []Target) (<-chan Result, <-chan error) {
	results := make(chan Result)
	errs := make(chan error, 1)

	go func() {
		defer close(results)
		defer close(errs)

		cache, err := OpenResultCache(settings)
		if err != nil {
			errs <- err
			return
		}
		var warnings []string
		if cache.Warning() != "" {
			warnings = append(warnings, cache.Warning())
		}

		var remaining []Target
		now := time.Now().UTC()
		if settings.ForceRecheck {
			remaining = append(remaining, targets...)
		} else {
			for _, target := range targets {
				if result, ok := cache.ReusableResult(target, now, settings.ExpiringSoonDays); ok {
					if !sendResult(ctx, results, result) {
						errs <- ctx.Err()
						return
					}
					continue
				}
				remaining = append(remaining, target)
			}
		}

		if len(remaining) == 0 {
			if err := cache.Save(); err != nil {
				errs <- WarningError(err.Error())
			}
			return
		}

		cloudflare, err := NewCloudflareSource(settings)
		if err != nil {
			errs <- err
			return
		}
		vercel, err := NewVercelSource(settings)
		if err != nil {
			errs <- err
			return
		}
		hostinger, err := NewHostingerSource(settings)
		if err != nil {
			errs <- err
			return
		}
		batches := targetBatches(remaining, settings.CloudflareBatchSize)
		for index, batch := range batches {
			if index > 0 && settings.RateLimit > 0 {
				timer := time.NewTimer(settings.RateLimit)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					errs <- nil
					return
				}
			}

			rdapResults, warning := runRDAPPrechecks(ctx, settings, batch)
			if warning != "" {
				warnings = append(warnings, warning)
			}

			var cloudflareTargets []Target
			for _, target := range batch {
				rdapResult := rdapResults[target.Domain]
				if shouldFinalizeFromRDAP(rdapResult) {
					result := finalizeRDAPPrecheck(target, rdapResult)
					cache.Store(result)
					if !sendResult(ctx, results, result) {
						errs <- nil
						return
					}
					continue
				}
				cloudflareTargets = append(cloudflareTargets, target)
			}

			var cloudflareQueue []cloudflareTask
			for _, target := range cloudflareTargets {
				cloudflareQueue = append(cloudflareQueue, cloudflareTask{
					target: target,
					rdap:   rdapResults[target.Domain],
				})
			}
			if len(cloudflareQueue) > 0 {
				if !drainSourceQueues(ctx, settings, cache, results, cloudflare, vercel, hostinger, cloudflareQueue) {
					errs <- nil
					return
				}
			}
		}

		if err := cache.Save(); err != nil {
			warnings = append(warnings, err.Error())
		}
		if len(warnings) > 0 {
			errs <- WarningError(strings.Join(warnings, "; "))
		}
	}()

	return results, errs
}

type cloudflareTask struct {
	target   Target
	rdap     Result
	attempts int
}

type fallbackTask struct {
	target   Target
	result   Result
	attempts int
}

func drainSourceQueues(
	ctx context.Context,
	settings Settings,
	cache *ResultCache,
	results chan<- Result,
	cloudflare *CloudflareSource,
	vercel *VercelSource,
	hostinger *HostingerSource,
	cloudflareQueue []cloudflareTask,
) bool {
	var vercelQueue []fallbackTask
	var hostingerQueue []fallbackTask
	var cloudflareBackoff sourceBackoff
	var vercelBackoff sourceBackoff
	var hostingerBackoff sourceBackoff
	vercelRateLimitAttempts := 0
	hostingerRateLimitAttempts := 0

	for len(cloudflareQueue) > 0 || len(vercelQueue) > 0 || len(hostingerQueue) > 0 {
		progressed := false
		now := time.Now().UTC()

		if len(cloudflareQueue) > 0 && !cloudflareBackoff.Active(now) {
			progressed = true
			batchSize := settings.CloudflareBatchSize
			if batchSize < 1 || batchSize > 20 {
				batchSize = 20
			}
			batch := popCloudflareTasks(&cloudflareQueue, batchSize)
			cfTargets := make([]Target, 0, len(batch))
			for _, task := range batch {
				cfTargets = append(cfTargets, task.target)
			}
			cloudflareResults := cloudflare.checkBatch(ctx, cfTargets)
			if retryAfter, rateLimited := sourceResultRetryAfter(cloudflareResults, settings.RateLimit); rateLimited {
				retryAfter = clampRetryAfter(retryAfter, settings.SourceRateLimitMaxDelay)
				cloudflareBackoff.Postpone(time.Now().UTC(), retryAfter)
				for _, task := range batch {
					cfResult := cloudflareResults[task.target.Domain]
					if task.attempts < settings.SourceRateLimitRetries {
						task.attempts++
						cloudflareQueue = append(cloudflareQueue, task)
						continue
					}
					result := ComposeResult(task.target, task.rdap, cfResult, settings)
					if !storeAndSendResult(ctx, cache, results, result) {
						return false
					}
				}
			} else {
				if settings.RateLimit > 0 {
					cloudflareBackoff.Postpone(time.Now().UTC(), settings.RateLimit)
				}

				for _, task := range batch {
					cfResult, ok := cloudflareResults[task.target.Domain]
					if !ok {
						cfResult = SourceResult{
							Name:         cloudflareSourceName,
							Availability: AvailabilityUnknown,
							Lifecycle:    "unknown",
							Confidence:   "unknown",
							Error:        "cloudflare omitted domain from response",
							CheckedAt:    time.Now().UTC(),
						}
					}
					result := ComposeResult(task.target, task.rdap, cfResult, settings)
					if result.Availability == AvailabilityUnknown {
						if vercel.Enabled() {
							vercelQueue = append(vercelQueue, fallbackTask{target: task.target, result: result})
						} else if hostinger.Enabled() {
							hostingerQueue = append(hostingerQueue, fallbackTask{target: task.target, result: result})
						} else if !storeAndSendResult(ctx, cache, results, result) {
							return false
						}
						continue
					}
					if !storeAndSendResult(ctx, cache, results, result) {
						return false
					}
				}
			}
		}

		now = time.Now().UTC()
		if len(vercelQueue) > 0 && !vercel.Enabled() {
			progressed = true
			for _, task := range vercelQueue {
				if hostinger.Enabled() {
					hostingerQueue = append(hostingerQueue, fallbackTask{target: task.target, result: task.result})
				} else if !storeAndSendResult(ctx, cache, results, task.result) {
					return false
				}
			}
			vercelQueue = nil
		} else if len(vercelQueue) > 0 && !vercelBackoff.Active(now) {
			progressed = true
			batchSize := settings.VercelBatchSize
			if batchSize < 1 || batchSize > 50 {
				batchSize = 50
			}
			batch := popFallbackTasks(&vercelQueue, batchSize)
			vercelTargets := make([]Target, 0, len(batch))
			for _, task := range batch {
				vercelTargets = append(vercelTargets, task.target)
			}
			vercelResults := vercel.Check(ctx, vercelTargets)
			if retryAfter, rateLimited := sourceResultRetryAfter(vercelResults, settings.VercelRateLimit); rateLimited {
				retryAfter = clampRetryAfter(retryAfter, settings.SourceRateLimitMaxDelay)
				vercelBackoff.Postpone(time.Now().UTC(), retryAfter)
				if hostinger.Enabled() {
					for _, task := range batch {
						vercelResult := vercelSourceResult(vercelResults, task.target)
						result := ComposeVercelFallback(task.result, vercelResult)
						hostingerQueue = append(hostingerQueue, fallbackTask{target: task.target, result: result})
					}
					for _, task := range vercelQueue {
						hostingerQueue = append(hostingerQueue, fallbackTask{target: task.target, result: task.result})
					}
					vercelQueue = nil
					continue
				}
				for _, task := range batch {
					vercelResult := vercelSourceResult(vercelResults, task.target)
					if vercelRateLimitAttempts < settings.SourceRateLimitRetries {
						vercelQueue = append(vercelQueue, task)
						continue
					}
					result := ComposeVercelFallback(task.result, vercelResult)
					if !storeAndSendResult(ctx, cache, results, result) {
						return false
					}
				}
				if vercelRateLimitAttempts < settings.SourceRateLimitRetries {
					vercelRateLimitAttempts++
					continue
				}
				if len(vercelQueue) > 0 {
					vercelResult := firstSourceResult(vercelResults, vercelSourceName)
					if !flushVercelRateLimited(ctx, cache, results, vercelQueue, vercelResult) {
						return false
					}
					vercelQueue = nil
				}
				continue
			}
			vercelRateLimitAttempts = 0
			if settings.VercelRateLimit > 0 {
				vercelBackoff.Postpone(time.Now().UTC(), settings.VercelRateLimit)
			}

			for _, task := range batch {
				vercelResult, ok := vercelResults[task.target.Domain]
				if !ok {
					vercelResult = SourceResult{
						Name:         vercelSourceName,
						Availability: AvailabilityUnknown,
						Lifecycle:    "unknown",
						Confidence:   "unknown",
						Error:        "Vercel omitted domain from response",
						CheckedAt:    time.Now().UTC(),
					}
				}
				result := ComposeVercelFallback(task.result, vercelResult)
				if result.Availability == AvailabilityUnknown && hostinger.Enabled() {
					hostingerQueue = append(hostingerQueue, fallbackTask{target: task.target, result: result})
					continue
				}
				if !storeAndSendResult(ctx, cache, results, result) {
					return false
				}
			}
		}

		now = time.Now().UTC()
		if len(hostingerQueue) > 0 && !hostinger.Enabled() {
			progressed = true
			for _, task := range hostingerQueue {
				if !storeAndSendResult(ctx, cache, results, task.result) {
					return false
				}
			}
			hostingerQueue = nil
		} else if len(hostingerQueue) > 0 && !hostingerBackoff.Active(now) {
			progressed = true
			task := popFallbackTasks(&hostingerQueue, 1)[0]
			hostingerResult := hostinger.Check(ctx, task.target)
			if retryAfter, rateLimited := sourceResultIsRateLimited(hostingerResult, settings.HostingerRateLimit); rateLimited {
				retryAfter = clampRetryAfter(retryAfter, settings.SourceRateLimitMaxDelay)
				hostingerBackoff.Postpone(time.Now().UTC(), retryAfter)
				if hostingerRateLimitAttempts < settings.SourceRateLimitRetries {
					hostingerRateLimitAttempts++
					hostingerQueue = append(hostingerQueue, task)
					continue
				}
				if !flushHostingerRateLimited(ctx, cache, results, append([]fallbackTask{task}, hostingerQueue...), hostingerResult) {
					return false
				}
				hostingerQueue = nil
				continue
			}
			hostingerRateLimitAttempts = 0
			if settings.HostingerRateLimit > 0 {
				hostingerBackoff.Postpone(time.Now().UTC(), settings.HostingerRateLimit)
			}
			result := ComposeHostingerFallback(task.result, hostingerResult)
			if !storeAndSendResult(ctx, cache, results, result) {
				return false
			}
		}

		if progressed {
			continue
		}

		wait := nextSourceBackoffDelay(time.Now().UTC(), cloudflareQueue, cloudflareBackoff, vercelQueue, vercelBackoff, hostingerQueue, hostingerBackoff)
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return false
		}
	}

	return true
}

func flushHostingerRateLimited(ctx context.Context, cache *ResultCache, results chan<- Result, tasks []fallbackTask, hostingerResult SourceResult) bool {
	for _, task := range tasks {
		result := ComposeHostingerFallback(task.result, hostingerResult)
		if !storeAndSendResult(ctx, cache, results, result) {
			return false
		}
	}
	return true
}

func flushVercelRateLimited(ctx context.Context, cache *ResultCache, results chan<- Result, tasks []fallbackTask, vercelResult SourceResult) bool {
	for _, task := range tasks {
		result := ComposeVercelFallback(task.result, vercelResult)
		if !storeAndSendResult(ctx, cache, results, result) {
			return false
		}
	}
	return true
}

func vercelSourceResult(sourceResults map[string]SourceResult, target Target) SourceResult {
	if result, ok := sourceResults[target.Domain]; ok {
		return result
	}
	return SourceResult{
		Name:         vercelSourceName,
		Availability: AvailabilityRateLimited,
		Lifecycle:    "rate_limited",
		Confidence:   "unknown",
		Error:        "Vercel rate limited",
		CheckedAt:    time.Now().UTC(),
	}
}

func firstSourceResult(sourceResults map[string]SourceResult, sourceName string) SourceResult {
	for _, result := range sourceResults {
		return result
	}
	return SourceResult{
		Name:         sourceName,
		Availability: AvailabilityRateLimited,
		Lifecycle:    "rate_limited",
		Confidence:   "unknown",
		Error:        sourceName + " rate limited",
		CheckedAt:    time.Now().UTC(),
	}
}

func popCloudflareTasks(queue *[]cloudflareTask, limit int) []cloudflareTask {
	if limit < 1 || limit > len(*queue) {
		limit = len(*queue)
	}
	batch := append([]cloudflareTask(nil), (*queue)[:limit]...)
	*queue = (*queue)[limit:]
	return batch
}

func popFallbackTasks(queue *[]fallbackTask, limit int) []fallbackTask {
	if limit < 1 || limit > len(*queue) {
		limit = len(*queue)
	}
	batch := append([]fallbackTask(nil), (*queue)[:limit]...)
	*queue = (*queue)[limit:]
	return batch
}

func storeAndSendResult(ctx context.Context, cache *ResultCache, results chan<- Result, result Result) bool {
	cache.Store(result)
	return sendResult(ctx, results, result)
}

func nextSourceBackoffDelay(
	now time.Time,
	cloudflareQueue []cloudflareTask,
	cloudflareBackoff sourceBackoff,
	vercelQueue []fallbackTask,
	vercelBackoff sourceBackoff,
	hostingerQueue []fallbackTask,
	hostingerBackoff sourceBackoff,
) time.Duration {
	var wait time.Duration
	set := false
	for _, item := range []struct {
		hasQueue bool
		backoff  sourceBackoff
	}{
		{hasQueue: len(cloudflareQueue) > 0, backoff: cloudflareBackoff},
		{hasQueue: len(vercelQueue) > 0, backoff: vercelBackoff},
		{hasQueue: len(hostingerQueue) > 0, backoff: hostingerBackoff},
	} {
		if !item.hasQueue {
			continue
		}
		delay := item.backoff.Delay(now)
		if delay <= 0 {
			return 0
		}
		if !set || delay < wait {
			wait = delay
			set = true
		}
	}
	return wait
}

func shouldFinalizeFromRDAP(result Result) bool {
	switch result.Availability {
	case AvailabilityRegistered, AvailabilityRedemption, AvailabilityPendingDelete:
		return true
	default:
		return false
	}
}

func finalizeRDAPPrecheck(target Target, rdap Result) Result {
	rdap.Input = target.Input
	rdap.LineNumber = target.LineNumber
	rdap.Sources = []SourceResult{SourceFromResult("rdap", rdap)}
	if rdap.Confidence == "" || rdap.Confidence == "authoritative" {
		rdap.Confidence = "rdap_precheck"
	}
	return rdap
}

func targetBatches(targets []Target, batchSize int) [][]Target {
	if batchSize < 1 || batchSize > 20 {
		batchSize = 20
	}
	batches := make([][]Target, 0, (len(targets)+batchSize-1)/batchSize)
	for start := 0; start < len(targets); start += batchSize {
		end := start + batchSize
		if end > len(targets) {
			end = len(targets)
		}
		batches = append(batches, targets[start:end])
	}
	return batches
}

func runRDAPPrechecks(ctx context.Context, settings Settings, targets []Target) (map[string]Result, string) {
	out := map[string]Result{}
	client, err := NewRDAPClient(settings)
	if err != nil {
		return rdapUnknownResults(targets, "create RDAP client: "+err.Error()), "RDAP pre-check failed: " + err.Error()
	}

	bootstrapLoad, err := client.LoadBootstrap(ctx)
	if err != nil {
		return rdapUnknownResults(targets, "load RDAP bootstrap: "+err.Error()), "RDAP pre-check failed: " + err.Error()
	}

	jobs := make(chan Target)
	var wg sync.WaitGroup
	var mu sync.Mutex
	limiter := rate.NewLimiter(rate.Inf, settings.Concurrency)
	if settings.RateLimit > 0 {
		limiter = rate.NewLimiter(rate.Every(settings.RateLimit), settings.Concurrency)
	}

	for i := 0; i < settings.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for target := range jobs {
				var result Result
				if err := limiter.Wait(ctx); err != nil {
					result = unknownResult(target, "", 0, err.Error(), time.Now().UTC())
				} else {
					result = client.QueryDomain(ctx, bootstrapLoad.Bootstrap, target)
				}
				mu.Lock()
				out[target.Domain] = result
				mu.Unlock()
			}
		}()
	}

	for _, target := range targets {
		select {
		case jobs <- target:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return out, ctx.Err().Error()
		}
	}
	close(jobs)
	wg.Wait()

	for _, target := range targets {
		if _, ok := out[target.Domain]; !ok {
			out[target.Domain] = unknownResult(target, "", 0, "RDAP pre-check did not complete", time.Now().UTC())
		}
	}

	return out, bootstrapLoad.Warning
}

func rdapUnknownResults(targets []Target, message string) map[string]Result {
	out := map[string]Result{}
	now := time.Now().UTC()
	for _, target := range targets {
		out[target.Domain] = unknownResult(target, "", 0, message, now)
	}
	return out
}

func sendResult(ctx context.Context, results chan<- Result, result Result) bool {
	select {
	case results <- result:
		return true
	case <-ctx.Done():
		return false
	}
}

func CollectResults(results <-chan Result, errs <-chan error) ([]Result, error) {
	var out []Result
	for result := range results {
		out = append(out, result)
	}
	err := <-errs
	SortResults(out)
	if warning, ok := err.(WarningError); ok {
		return out, warning
	}
	return out, err
}

type WarningError string

func (e WarningError) Error() string {
	return string(e)
}

func SortResults(results []Result) {
	sort.SliceStable(results, func(i, j int) bool {
		leftRank := resultSortRank(results[i])
		rightRank := resultSortRank(results[j])
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return results[i].Domain < results[j].Domain
	})
}
