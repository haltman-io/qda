package sources

import (
	"time"

	"qda/internal/config"
	"qda/internal/types"
)

// Compose merges the RDAP verdict with a registrar source verdict into the
// final result for a domain.
func Compose(target types.Target, rdap types.Result, source types.SourceResult, settings config.Settings) types.Result {
	now := time.Now().UTC()
	sources := rdap.Sources
	if len(sources) == 0 && rdap.Source != "" {
		sources = []types.SourceResult{types.SourceFromResult("rdap", rdap)}
	}
	sources = append(sources, source)

	if source.Error != "" && source.Availability != types.AvailabilityRateLimited {
		return composeSourceFailure(target, rdap, source, sources, settings, now)
	}

	result := types.Result{
		Domain:       target.Domain,
		Input:        target.Input,
		LineNumber:   target.LineNumber,
		Availability: source.Availability,
		Lifecycle:    source.Lifecycle,
		Confidence:   source.Confidence,
		Source:       source.Source,
		Sources:      sources,
		HTTPStatus:   source.HTTPStatus,
		CheckedAt:    now,
		Price:        source.Pricing,
	}

	if source.Registrable != nil && *source.Registrable {
		result.Availability = types.AvailabilityAvailable
		result.Lifecycle = "available"
		result.Confidence = source.Name + "_authoritative"
		return result
	}

	switch source.Reason {
	case "domain_premium":
		result.Availability = types.AvailabilityPremium
		result.Lifecycle = "premium"
		result.Confidence = source.Name + "_authoritative"
	case "domain_unavailable":
		if rdap.Availability == types.AvailabilityRegistered ||
			rdap.Availability == types.AvailabilityRedemption ||
			rdap.Availability == types.AvailabilityPendingDelete {
			copyRDAPRegistrationFields(&result, rdap, settings)
			result.Availability = rdap.Availability
			result.Confidence = source.Name + "_rdap"
			if result.Lifecycle == "" {
				result.Lifecycle = "registered"
			}
		} else {
			result.Availability = types.AvailabilityReserved
			result.Lifecycle = "not_registrable"
			result.Confidence = source.Name + "_authoritative"
		}
	case "extension_disallows_registration":
		result.Availability = types.AvailabilityReserved
		result.Lifecycle = "extension_disallows_registration"
		result.Confidence = source.Name + "_authoritative"
	case "extension_not_supported", "extension_not_supported_via_api":
		result.Availability = types.AvailabilityUnknown
		result.Lifecycle = source.Reason
		result.Confidence = "unsupported"
	default:
		if source.Availability == types.AvailabilityReserved {
			result.Confidence = source.Name + "_authoritative"
		}
	}

	return result
}

// ComposeRateLimited builds the final result when a fallback source is
// rate limited.
func ComposeRateLimited(target types.Target, rdap types.Result, source types.SourceResult) types.Result {
	sources := rdap.Sources
	if len(sources) == 0 && rdap.Source != "" {
		sources = []types.SourceResult{types.SourceFromResult("rdap", rdap)}
	}
	sources = append(sources, source)
	return types.Result{
		Domain:        target.Domain,
		Input:         target.Input,
		LineNumber:    target.LineNumber,
		Availability:  types.AvailabilityRateLimited,
		Lifecycle:     "rate_limited",
		Confidence:    "unknown",
		Source:        source.Source,
		Sources:       sources,
		HTTPStatus:    source.HTTPStatus,
		Error:         source.Name + ": " + source.Error,
		CheckedAt:     time.Now().UTC(),
		RetryAfter:    source.RetryAfter,
		RetryAfterSet: source.RetryAfterSet,
	}
}

func composeSourceFailure(target types.Target, rdap types.Result, source types.SourceResult, sources []types.SourceResult, settings config.Settings, now time.Time) types.Result {
	if rdap.Availability == types.AvailabilityRegistered ||
		rdap.Availability == types.AvailabilityRedemption ||
		rdap.Availability == types.AvailabilityPendingDelete {
		result := rdap
		result.Input = target.Input
		result.LineNumber = target.LineNumber
		result.Sources = sources
		result.Confidence = "rdap_only_" + source.Name + "_failed"
		result.Error = types.AppendError(result.Error, source.Name+": "+source.Error)
		return result
	}

	message := source.Name + ": " + source.Error
	if rdap.Availability == types.AvailabilityAvailable {
		message = types.AppendError(message, "rdap reported available, but "+source.Name+" did not confirm registrability")
	} else if rdap.Error != "" {
		message = types.AppendError(message, "rdap: "+rdap.Error)
	}

	return types.Result{
		Domain:       target.Domain,
		Input:        target.Input,
		LineNumber:   target.LineNumber,
		Availability: types.AvailabilityUnknown,
		Lifecycle:    source.Name + "_unconfirmed",
		Confidence:   "unknown",
		Source:       source.Source,
		Sources:      sources,
		HTTPStatus:   source.HTTPStatus,
		Error:        message,
		CheckedAt:    now,
	}
}

func copyRDAPRegistrationFields(result *types.Result, rdap types.Result, settings config.Settings) {
	result.CreatedAt = rdap.CreatedAt
	result.ExpiresAt = rdap.ExpiresAt
	result.UpdatedAt = rdap.UpdatedAt
	result.Statuses = append([]string(nil), rdap.Statuses...)
	result.Registrar = rdap.Registrar
	result.Lifecycle = rdap.Lifecycle
	result.ExpiringSoon = rdap.ExpiringSoon
	result.ExpiresInDays = rdap.ExpiresInDays
	if result.Lifecycle == "" {
		lifecycle, expiringSoon, expiresInDays := types.DetermineLifecycle(result.Availability, result.Statuses, result.ExpiresAt, settings.ExpiringSoonDays, time.Now().UTC())
		result.Lifecycle = lifecycle
		result.ExpiringSoon = expiringSoon
		result.ExpiresInDays = expiresInDays
	}
}
