package qda

import (
	"strings"
	"time"
)

func ComposeResult(target Target, rdap Result, cloudflare SourceResult, settings Settings) Result {
	now := time.Now().UTC()
	sources := []SourceResult{SourceFromResult("rdap", rdap), cloudflare}

	if cloudflare.Error != "" {
		return composeCloudflareFailure(target, rdap, cloudflare, sources, now)
	}

	result := Result{
		Domain:                target.Domain,
		Input:                 target.Input,
		LineNumber:            target.LineNumber,
		Availability:          cloudflare.Availability,
		Lifecycle:             cloudflare.Lifecycle,
		Confidence:            cloudflare.Confidence,
		Source:                cloudflare.Source,
		Sources:               sources,
		HTTPStatus:            cloudflare.HTTPStatus,
		CheckedAt:             now,
		CloudflareRegistrable: cloudflare.Registrable,
		CloudflareReason:      cloudflare.Reason,
		CloudflareTier:        cloudflare.Tier,
		CloudflarePricing:     cloudflare.Pricing,
	}

	if cloudflare.Registrable != nil && *cloudflare.Registrable {
		result.Availability = AvailabilityAvailable
		result.Lifecycle = "available"
		result.Confidence = "cloudflare_authoritative"
		return result
	}

	switch cloudflare.Reason {
	case "domain_premium":
		result.Availability = AvailabilityPremium
		result.Lifecycle = "premium"
		result.Confidence = "cloudflare_authoritative"
	case "domain_unavailable":
		if rdap.Availability == AvailabilityRegistered ||
			rdap.Availability == AvailabilityRedemption ||
			rdap.Availability == AvailabilityPendingDelete {
			copyRDAPRegistrationFields(&result, rdap, settings)
			result.Availability = rdap.Availability
			result.Confidence = "cloudflare_rdap"
			result.Source = cloudflare.Source
			if result.Lifecycle == "" {
				result.Lifecycle = "registered"
			}
		} else {
			result.Availability = AvailabilityReserved
			result.Lifecycle = "not_registrable"
			result.Confidence = "cloudflare_authoritative"
		}
	case "extension_disallows_registration":
		result.Availability = AvailabilityReserved
		result.Lifecycle = "extension_disallows_registration"
		result.Confidence = "cloudflare_authoritative"
	case "extension_not_supported", "extension_not_supported_via_api":
		result.Availability = AvailabilityUnknown
		result.Lifecycle = cloudflare.Reason
		result.Confidence = "unsupported_by_cloudflare"
	default:
		result.Availability = AvailabilityUnknown
		result.Lifecycle = "unknown"
		result.Confidence = "unknown"
	}

	return result
}

func ComposeHostingerFallback(result Result, hostinger SourceResult) Result {
	result.Sources = append(result.Sources, hostinger)
	result.HostingerAvailable = hostinger.Registrable
	result.HostingerRestriction = hostinger.Reason

	if hostinger.Availability == AvailabilityRateLimited {
		result.Availability = AvailabilityRateLimited
		result.Lifecycle = "rate_limited"
		result.Confidence = "unknown"
		result.HTTPStatus = hostinger.HTTPStatus
		result.Error = appendError(result.Error, "hostinger: "+hostinger.Error)
		return result
	}

	if hostinger.Error != "" {
		result.Error = appendError(result.Error, "hostinger: "+hostinger.Error)
		return result
	}

	result.Source = hostinger.Source
	result.HTTPStatus = hostinger.HTTPStatus
	result.CheckedAt = time.Now().UTC()

	if hostinger.Registrable != nil && *hostinger.Registrable {
		result.Availability = AvailabilityAvailable
		result.Lifecycle = "available"
		result.Confidence = "hostinger_authoritative"
		result.Error = ""
		return result
	}

	if hostinger.Availability == AvailabilityReserved {
		result.Availability = AvailabilityReserved
		result.Lifecycle = hostinger.Lifecycle
		result.Confidence = "hostinger_authoritative"
		result.Error = ""
		return result
	}

	return result
}

func ComposeVercelFallback(result Result, vercel SourceResult) Result {
	result.Sources = append(result.Sources, vercel)
	result.VercelAvailable = vercel.Registrable
	result.VercelPricing = vercel.Pricing

	if vercel.Availability == AvailabilityRateLimited {
		result.Availability = AvailabilityRateLimited
		result.Lifecycle = "rate_limited"
		result.Confidence = "unknown"
		result.HTTPStatus = vercel.HTTPStatus
		result.Error = appendError(result.Error, "vercel: "+vercel.Error)
		return result
	}

	if vercel.Error != "" {
		result.Error = appendError(result.Error, "vercel: "+vercel.Error)
		return result
	}

	result.Source = vercel.Source
	result.HTTPStatus = vercel.HTTPStatus
	result.CheckedAt = time.Now().UTC()

	if vercel.Registrable != nil && *vercel.Registrable {
		result.Availability = AvailabilityAvailable
		result.Lifecycle = "available"
		result.Confidence = "vercel_authoritative"
		result.Error = ""
		return result
	}

	if vercel.Availability == AvailabilityReserved {
		result.Availability = AvailabilityReserved
		result.Lifecycle = vercel.Lifecycle
		result.Confidence = "vercel_authoritative"
		result.Error = ""
		return result
	}

	return result
}

func composeCloudflareFailure(target Target, rdap Result, cloudflare SourceResult, sources []SourceResult, now time.Time) Result {
	if cloudflare.Availability == AvailabilityRateLimited {
		return Result{
			Domain:       target.Domain,
			Input:        target.Input,
			LineNumber:   target.LineNumber,
			Availability: AvailabilityRateLimited,
			Lifecycle:    "rate_limited",
			Confidence:   "unknown",
			Source:       cloudflare.Source,
			Sources:      sources,
			HTTPStatus:   cloudflare.HTTPStatus,
			Error:        "cloudflare: " + cloudflare.Error,
			CheckedAt:    now,
		}
	}

	if rdap.Availability == AvailabilityRegistered ||
		rdap.Availability == AvailabilityRedemption ||
		rdap.Availability == AvailabilityPendingDelete {
		result := rdap
		result.Input = target.Input
		result.LineNumber = target.LineNumber
		result.Sources = sources
		result.Confidence = "rdap_only_cloudflare_failed"
		result.Error = appendError(result.Error, "cloudflare: "+cloudflare.Error)
		return result
	}

	message := "cloudflare: " + cloudflare.Error
	if rdap.Availability == AvailabilityAvailable {
		message = appendError(message, "rdap reported available, but Cloudflare did not confirm registrability")
	} else if rdap.Error != "" {
		message = appendError(message, "rdap: "+rdap.Error)
	}

	return Result{
		Domain:       target.Domain,
		Input:        target.Input,
		LineNumber:   target.LineNumber,
		Availability: AvailabilityUnknown,
		Lifecycle:    "cloudflare_unconfirmed",
		Confidence:   "unknown",
		Source:       cloudflare.Source,
		Sources:      sources,
		HTTPStatus:   cloudflare.HTTPStatus,
		Error:        message,
		CheckedAt:    now,
	}
}

func copyRDAPRegistrationFields(result *Result, rdap Result, settings Settings) {
	result.CreatedAt = rdap.CreatedAt
	result.ExpiresAt = rdap.ExpiresAt
	result.UpdatedAt = rdap.UpdatedAt
	result.RDAPUpdatedAt = rdap.RDAPUpdatedAt
	result.Statuses = append([]string(nil), rdap.Statuses...)
	result.Registrar = rdap.Registrar
	result.Lifecycle = rdap.Lifecycle
	result.ExpiringSoon = rdap.ExpiringSoon
	result.ExpiresInDays = rdap.ExpiresInDays
	if result.Lifecycle == "" {
		lifecycle, expiringSoon, expiresInDays := DetermineLifecycle(result.Availability, result.Statuses, result.ExpiresAt, settings.ExpiringSoonDays, time.Now().UTC())
		result.Lifecycle = lifecycle
		result.ExpiringSoon = expiringSoon
		result.ExpiresInDays = expiresInDays
	}
}

func appendError(current string, next string) string {
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
