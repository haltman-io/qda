package qda

import (
	"strings"
	"time"
)

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

type Result struct {
	Domain                string         `json:"domain"`
	Input                 string         `json:"input,omitempty"`
	LineNumber            int            `json:"line_number,omitempty"`
	Availability          Availability   `json:"availability"`
	Lifecycle             string         `json:"lifecycle"`
	Confidence            string         `json:"confidence"`
	CreatedAt             string         `json:"created_at,omitempty"`
	ExpiresAt             string         `json:"expires_at,omitempty"`
	UpdatedAt             string         `json:"updated_at,omitempty"`
	RDAPUpdatedAt         string         `json:"rdap_updated_at,omitempty"`
	Statuses              []string       `json:"statuses,omitempty"`
	Registrar             string         `json:"registrar,omitempty"`
	Source                string         `json:"source,omitempty"`
	Sources               []SourceResult `json:"sources,omitempty"`
	HTTPStatus            int            `json:"http_status,omitempty"`
	Error                 string         `json:"error,omitempty"`
	CheckedAt             time.Time      `json:"checked_at"`
	CacheHit              bool           `json:"cache_hit,omitempty"`
	CacheReason           string         `json:"cache_reason,omitempty"`
	ExpiringSoon          bool           `json:"expiring_soon"`
	ExpiresInDays         *int           `json:"expires_in_days,omitempty"`
	CloudflareRegistrable *bool          `json:"cloudflare_registrable,omitempty"`
	CloudflareReason      string         `json:"cloudflare_reason,omitempty"`
	CloudflareTier        string         `json:"cloudflare_tier,omitempty"`
	CloudflarePricing     *Pricing       `json:"cloudflare_pricing,omitempty"`
	VercelAvailable       *bool          `json:"vercel_available,omitempty"`
	VercelPricing         *Pricing       `json:"vercel_pricing,omitempty"`
	HostingerAvailable    *bool          `json:"hostinger_available,omitempty"`
	HostingerRestriction  string         `json:"hostinger_restriction,omitempty"`
}

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
	Registrable   *bool         `json:"registrable,omitempty"`
	Reason        string        `json:"reason,omitempty"`
	Tier          string        `json:"tier,omitempty"`
	Pricing       *Pricing      `json:"pricing,omitempty"`
}

type Pricing struct {
	Currency         string `json:"currency,omitempty"`
	RegistrationCost string `json:"registration_cost,omitempty"`
	RenewalCost      string `json:"renewal_cost,omitempty"`
	TransferCost     string `json:"transfer_cost,omitempty"`
	Years            string `json:"years,omitempty"`
}

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

func ClassifyAvailability(statuses []string) Availability {
	normalized := normalizedStatusSet(statuses)
	if normalized["redemptionperiod"] {
		return AvailabilityRedemption
	}
	if normalized["pendingdelete"] {
		return AvailabilityPendingDelete
	}
	return AvailabilityRegistered
}

func DetermineLifecycle(availability Availability, statuses []string, expiresAt string, soonDays int, now time.Time) (string, bool, *int) {
	switch availability {
	case AvailabilityAvailable:
		return "available", false, nil
	case AvailabilityRedemption:
		return "redemption", false, daysUntil(expiresAt, now)
	case AvailabilityPendingDelete:
		return "pending_delete", false, daysUntil(expiresAt, now)
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

	days := daysUntil(expiresAt, now)
	normalized := normalizedStatusSet(statuses)
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

func SourceFromResult(name string, result Result) SourceResult {
	return SourceResult{
		Name:         name,
		Availability: result.Availability,
		Lifecycle:    result.Lifecycle,
		Confidence:   result.Confidence,
		Source:       result.Source,
		HTTPStatus:   result.HTTPStatus,
		Error:        result.Error,
		CheckedAt:    result.CheckedAt,
	}
}

func normalizedStatusSet(statuses []string) map[string]bool {
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

func daysUntil(value string, now time.Time) *int {
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

func resultSortRank(result Result) int {
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

func IsAvailableLike(result Result) bool {
	return result.Availability == AvailabilityAvailable || result.Availability == AvailabilityPremium
}

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
