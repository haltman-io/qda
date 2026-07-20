// Package types holds the core domain model shared by every qda package.
package types

import (
	"strings"
	"time"
)

// Availability is the conservative status model used across all sources.
type Availability string

const (
	AvailabilityAvailable     Availability = "available"
	AvailabilityRegistered    Availability = "registered"
	AvailabilityReserved      Availability = "reserved"
	AvailabilityPremium       Availability = "premium"
	AvailabilityRedemption    Availability = "redemption"
	AvailabilityPendingDelete Availability = "pending_delete"
	AvailabilityRateLimited   Availability = "rate_limited"
	AvailabilityUnknown       Availability = "unknown"
	AvailabilityInvalid       Availability = "invalid"
)

// Target is a single domain queued for checking.
type Target struct {
	Domain     string
	Input      string
	LineNumber int
}

// SkippedInput is an input line that could not be converted into a target.
type SkippedInput struct {
	Input      string `json:"input"`
	LineNumber int    `json:"line_number"`
	Reason     string `json:"reason"`
}

// Pricing carries registrar price information when available.
type Pricing struct {
	Currency         string `json:"currency,omitempty"`
	RegistrationCost string `json:"registration_cost,omitempty"`
	RenewalCost      string `json:"renewal_cost,omitempty"`
	TransferCost     string `json:"transfer_cost,omitempty"`
	Years            string `json:"years,omitempty"`
}

// SourceResult is the verdict of a single source for a single domain.
type SourceResult struct {
	Name          string        `json:"name"`
	Availability  Availability  `json:"availability"`
	Lifecycle     string        `json:"lifecycle,omitempty"`
	Confidence    string        `json:"confidence,omitempty"`
	Source        string        `json:"source,omitempty"`
	HTTPStatus    int           `json:"http_status,omitempty"`
	Error         string        `json:"error,omitempty"`
	CheckedAt     time.Time     `json:"checked_at"`
	RetryAfter    time.Duration `json:"-"`
	RetryAfterSet bool          `json:"-"`
	Retryable     bool          `json:"-"`
	Registrable   *bool         `json:"registrable,omitempty"`
	Reason        string        `json:"reason,omitempty"`
	Tier          string        `json:"tier,omitempty"`
	Pricing       *Pricing      `json:"pricing,omitempty"`
}

// Result is the consolidated verdict for a domain.
type Result struct {
	Domain         string         `json:"domain"`
	Input          string         `json:"input,omitempty"`
	LineNumber     int            `json:"line_number,omitempty"`
	Availability   Availability   `json:"availability"`
	Lifecycle      string         `json:"lifecycle"`
	Confidence     string         `json:"confidence"`
	CreatedAt      string         `json:"created_at,omitempty"`
	ExpiresAt      string         `json:"expires_at,omitempty"`
	UpdatedAt      string         `json:"updated_at,omitempty"`
	Statuses       []string       `json:"statuses,omitempty"`
	Registrar      string         `json:"registrar,omitempty"`
	Source         string         `json:"source,omitempty"`
	Sources        []SourceResult `json:"sources,omitempty"`
	HTTPStatus     int            `json:"http_status,omitempty"`
	Error          string         `json:"error,omitempty"`
	RetryAfter     time.Duration  `json:"-"`
	RetryAfterSet  bool           `json:"-"`
	Retryable      bool           `json:"-"`
	CheckedAt      time.Time      `json:"checked_at"`
	CacheHit       bool           `json:"cache_hit,omitempty"`
	ExpiringSoon   bool           `json:"expiring_soon"`
	ExpiresInDays  *int           `json:"expires_in_days,omitempty"`
	Price          *Pricing       `json:"price,omitempty"`
	DurationMillis int64          `json:"duration_ms,omitempty"`
}

// InvalidResult converts a skipped input line into a Result.
func InvalidResult(skipped SkippedInput) Result {
	return Result{
		Domain:       skipped.Input,
		Input:        skipped.Input,
		LineNumber:   skipped.LineNumber,
		Availability: AvailabilityInvalid,
		Lifecycle:    "invalid",
		Confidence:   "none",
		Error:        skipped.Reason,
		CheckedAt:    time.Now().UTC(),
	}
}

// ClassifyAvailability maps RDAP status values to Availability.
func ClassifyAvailability(statuses []string) Availability {
	normalized := NormalizedStatusSet(statuses)
	if normalized["redemptionperiod"] {
		return AvailabilityRedemption
	}
	if normalized["pendingdelete"] {
		return AvailabilityPendingDelete
	}
	return AvailabilityRegistered
}

// DetermineLifecycle derives lifecycle, expiring-soon flag and days to expiry.
func DetermineLifecycle(availability Availability, statuses []string, expiresAt string, soonDays int, now time.Time) (string, bool, *int) {
	switch availability {
	case AvailabilityAvailable:
		return "available", false, nil
	case AvailabilityRedemption:
		return "redemption", false, DaysUntil(expiresAt, now)
	case AvailabilityPendingDelete:
		return "pending_delete", false, DaysUntil(expiresAt, now)
	case AvailabilityRateLimited:
		return "rate_limited", false, nil
	case AvailabilityInvalid:
		return "invalid", false, nil
	case AvailabilityUnknown:
		return "unknown", false, nil
	case AvailabilityReserved:
		return "reserved", false, nil
	case AvailabilityPremium:
		return "premium", false, nil
	}

	days := DaysUntil(expiresAt, now)
	normalized := NormalizedStatusSet(statuses)
	if days != nil {
		if *days < 0 {
			if normalized["autorenewperiod"] {
				return "expired_auto_renew_grace", false, days
			}
			return "expired_or_grace", false, days
		}
		if *days <= soonDays {
			return "expiring_soon", true, days
		}
	}

	if normalized["clienthold"] || normalized["serverhold"] {
		return "hold", false, days
	}
	return "active", false, days
}

// SourceFromResult flattens a Result into a SourceResult entry.
func SourceFromResult(name string, result Result) SourceResult {
	return SourceResult{
		Name:          name,
		Availability:  result.Availability,
		Lifecycle:     result.Lifecycle,
		Confidence:    result.Confidence,
		Source:        result.Source,
		HTTPStatus:    result.HTTPStatus,
		Error:         result.Error,
		CheckedAt:     result.CheckedAt,
		RetryAfter:    result.RetryAfter,
		RetryAfterSet: result.RetryAfterSet,
	}
}

// NormalizedStatusSet lowercases statuses and strips spaces, dashes and underscores.
func NormalizedStatusSet(statuses []string) map[string]bool {
	out := map[string]bool{}
	for _, status := range statuses {
		key := strings.ToLower(status)
		key = strings.ReplaceAll(key, " ", "")
		key = strings.ReplaceAll(key, "-", "")
		key = strings.ReplaceAll(key, "_", "")
		if key != "" {
			out[key] = true
		}
	}
	return out
}

// DaysUntil returns whole days between now and an RFC3339 timestamp.
func DaysUntil(value string, now time.Time) *int {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil
	}
	days := int(t.Sub(now).Hours() / 24)
	return &days
}

// IsAvailableLike reports whether the result is directly registrable.
func IsAvailableLike(result Result) bool {
	return result.Availability == AvailabilityAvailable || result.Availability == AvailabilityPremium
}

// IsAvailableSoon reports whether the domain may become available shortly.
func IsAvailableSoon(result Result) bool {
	switch result.Availability {
	case AvailabilityPendingDelete, AvailabilityRedemption:
		return true
	}
	if result.ExpiringSoon {
		return true
	}
	switch result.Lifecycle {
	case "expiring_soon", "expired_or_grace", "expired_auto_renew_grace", "pending_delete", "redemption":
		return true
	default:
		return false
	}
}

// SortRank orders results for prioritized display.
func SortRank(result Result) int {
	if IsAvailableLike(result) {
		return 0
	}
	if IsAvailableSoon(result) {
		return 1
	}
	switch result.Availability {
	case AvailabilityRateLimited:
		return 2
	case AvailabilityUnknown, AvailabilityReserved, AvailabilityInvalid:
		return 3
	case AvailabilityRegistered:
		return 4
	default:
		return 9
	}
}

// AppendError joins error messages with "; " skipping empty parts.
func AppendError(current string, next string) string {
	current = strings.TrimSpace(current)
	next = strings.TrimSpace(next)
	if current == "" {
		return next
	}
	if next == "" {
		return current
	}
	return current + "; " + next
}
