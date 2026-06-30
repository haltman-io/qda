package qda

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintSummaryIncludesPrioritizedFinalTable(t *testing.T) {
	results := []Result{
		{Domain: "free.com", Availability: AvailabilityAvailable, Lifecycle: "available"},
		{Domain: "taken.com", Availability: AvailabilityRegistered, Lifecycle: "active", ExpiresAt: "2030-01-01T00:00:00Z"},
		{Domain: "drop.com", Availability: AvailabilityPendingDelete, Lifecycle: "pending_delete"},
		{Domain: "redeem.com", Availability: AvailabilityRedemption, Lifecycle: "redemption"},
		{Domain: "limited.com", Availability: AvailabilityRateLimited, Lifecycle: "rate_limited"},
		{Domain: "unknown.com", Availability: AvailabilityUnknown, Lifecycle: "unknown"},
		{Domain: "premium.com", Availability: AvailabilityPremium, Lifecycle: "premium"},
	}

	var out bytes.Buffer
	PrintSummary(&out, results)
	text := stripTestANSI(out.String())

	for _, want := range []string{
		"FINAL RESULTS",
		"AVAILABLE",
		"AVAILABLE PREMIUM",
		"AVAILABLE SOON",
		"RATE LIMITED",
		"UNKNOWN",
		"REGISTERED",
		"free.com",
		"premium.com",
		"drop.com",
		"redeem.com",
		"limited.com",
		"unknown.com",
		"taken.com",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}

	if strings.Index(text, "free.com") > strings.Index(text, "taken.com") {
		t.Fatalf("available domain should be listed before registered domain:\n%s", text)
	}
}

func TestPrintSummaryCanHideRegisteredAndReservedDomains(t *testing.T) {
	results := []Result{
		{Domain: "free.com", Availability: AvailabilityAvailable, Lifecycle: "available"},
		{Domain: "taken.com", Availability: AvailabilityRegistered, Lifecycle: "active"},
		{Domain: "reserved.com", Availability: AvailabilityReserved, Lifecycle: "reserved"},
		{Domain: "limited.com", Availability: AvailabilityRateLimited, Lifecycle: "rate_limited"},
	}

	var out bytes.Buffer
	PrintSummaryWithSettings(&out, results, Settings{HideRegisteredReserved: true})
	text := stripTestANSI(out.String())

	for _, want := range []string{"free.com", "limited.com"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	for _, hidden := range []string{"taken.com", "reserved.com"} {
		if strings.Contains(text, hidden) {
			t.Fatalf("did not hide %q in:\n%s", hidden, text)
		}
	}
	if !strings.Contains(text, "1 registered") || !strings.Contains(text, "1 reserved") {
		t.Fatalf("summary counts should still include hidden statuses:\n%s", text)
	}
}

func stripTestANSI(value string) string {
	replacer := strings.NewReplacer(
		ansiReset, "",
		ansiBold, "",
		ansiGreen, "",
		ansiYellow, "",
		ansiRed, "",
		ansiBlue, "",
		ansiMagenta, "",
		ansiGray, "",
		ansiMutedGray, "",
	)
	return replacer.Replace(value)
}
