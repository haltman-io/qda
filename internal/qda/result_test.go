package qda

import (
	"testing"
	"time"
)

func TestClassifyAvailabilityLifecycle(t *testing.T) {
	if got := ClassifyAvailability([]string{"redemptionPeriod"}); got != AvailabilityRedemption {
		t.Fatalf("got %q", got)
	}
	if got := ClassifyAvailability([]string{"pending delete"}); got != AvailabilityPendingDelete {
		t.Fatalf("got %q", got)
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	lifecycle, soon, days := DetermineLifecycle(AvailabilityRegistered, []string{"ok"}, "2026-01-10T00:00:00Z", 30, now)
	if lifecycle != "expiring_soon" || !soon || days == nil || *days != 9 {
		t.Fatalf("unexpected lifecycle=%q soon=%v days=%v", lifecycle, soon, days)
	}
}
