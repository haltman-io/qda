package qda

import (
	"testing"
	"time"
)

func TestClampRetryAfterCapsLargeSourceDelay(t *testing.T) {
	if got := clampRetryAfter(60*time.Second, 5*time.Second); got != 5*time.Second {
		t.Fatalf("got %s", got)
	}
}

func TestClampRetryAfterAllowsDisabledCap(t *testing.T) {
	if got := clampRetryAfter(60*time.Second, 0); got != 60*time.Second {
		t.Fatalf("got %s", got)
	}
}
