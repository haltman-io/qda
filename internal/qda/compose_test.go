package qda

import "testing"

func TestComposeCloudflareAvailableWins(t *testing.T) {
	registrable := true
	result := ComposeResult(
		Target{Domain: "brand.com"},
		Result{Domain: "brand.com", Availability: AvailabilityUnknown, Lifecycle: "unknown"},
		SourceResult{
			Name:         cloudflareSourceName,
			Availability: AvailabilityAvailable,
			Lifecycle:    "available",
			Confidence:   "authoritative",
			Registrable:  &registrable,
		},
		DefaultSettings(),
	)

	if result.Availability != AvailabilityAvailable {
		t.Fatalf("got %q", result.Availability)
	}
	if result.CloudflareRegistrable == nil || !*result.CloudflareRegistrable {
		t.Fatalf("missing cloudflare registrable field: %#v", result)
	}
}

func TestComposeDomainUnavailableUsesRDAPRegistration(t *testing.T) {
	registrable := false
	result := ComposeResult(
		Target{Domain: "example.com"},
		Result{
			Domain:       "example.com",
			Availability: AvailabilityRegistered,
			Lifecycle:    "active",
			ExpiresAt:    "2030-01-01T00:00:00Z",
		},
		SourceResult{
			Name:         cloudflareSourceName,
			Availability: AvailabilityReserved,
			Lifecycle:    "not_registrable",
			Confidence:   "authoritative",
			Registrable:  &registrable,
			Reason:       "domain_unavailable",
		},
		DefaultSettings(),
	)

	if result.Availability != AvailabilityRegistered {
		t.Fatalf("got %q", result.Availability)
	}
	if result.ExpiresAt == "" {
		t.Fatal("expected RDAP expiration to be preserved")
	}
}

func TestComposeCloudflareFailureDoesNotTrustRDAPAvailable(t *testing.T) {
	result := ComposeResult(
		Target{Domain: "brand.com"},
		Result{Domain: "brand.com", Availability: AvailabilityAvailable, Lifecycle: "available"},
		SourceResult{
			Name:         cloudflareSourceName,
			Availability: AvailabilityUnknown,
			Error:        "timeout",
		},
		DefaultSettings(),
	)

	if result.Availability != AvailabilityUnknown {
		t.Fatalf("got %q", result.Availability)
	}
}

func TestComposeVercelFallbackAvailable(t *testing.T) {
	registrable := true
	base := Result{
		Domain:       "brand.com",
		Availability: AvailabilityUnknown,
		Lifecycle:    "extension_not_supported_via_api",
		Confidence:   "unsupported_by_cloudflare",
	}

	result := ComposeVercelFallback(base, SourceResult{
		Name:         vercelSourceName,
		Availability: AvailabilityAvailable,
		Lifecycle:    "available",
		Confidence:   "vercel_authoritative",
		Source:       "https://api.vercel.com/v1/registrar/domains/availability",
		Registrable:  &registrable,
		Pricing: &Pricing{
			RegistrationCost: "12.34",
			RenewalCost:      "13.5",
		},
	})

	if result.Availability != AvailabilityAvailable {
		t.Fatalf("got %q", result.Availability)
	}
	if result.Confidence != "vercel_authoritative" {
		t.Fatalf("got confidence %q", result.Confidence)
	}
	if result.VercelAvailable == nil || !*result.VercelAvailable {
		t.Fatalf("missing Vercel availability: %#v", result)
	}
	if result.VercelPricing == nil || result.VercelPricing.RegistrationCost != "12.34" {
		t.Fatalf("missing Vercel pricing: %#v", result.VercelPricing)
	}
}
