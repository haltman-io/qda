package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestWaitPacesRequests(t *testing.T) {
	controller := NewController()
	controller.SetInterval("k", 50*time.Millisecond)

	ctx := context.Background()
	if err := controller.Wait(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := controller.Wait(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("second wait returned too early: %s", elapsed)
	}
}

func TestFreezeBlocksUntilExpiry(t *testing.T) {
	controller := NewController()
	controller.Freeze("k", 120*time.Millisecond)
	if !controller.Frozen("k") {
		t.Fatal("key should be frozen")
	}

	start := time.Now()
	if err := controller.Wait(context.Background(), "k"); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("wait returned before freeze expiry: %s", elapsed)
	}
	if controller.Frozen("k") {
		t.Fatal("freeze should have expired")
	}
}

func TestFreezeDoesNotShorten(t *testing.T) {
	controller := NewController()
	controller.Freeze("k", 500*time.Millisecond)
	first := controller.FrozenUntil("k")
	controller.Freeze("k", 10*time.Millisecond)
	if !controller.FrozenUntil("k").Equal(first) {
		t.Fatal("shorter freeze shortened the active freeze")
	}
}

func TestOtherKeysKeepFlowing(t *testing.T) {
	controller := NewController()
	controller.Freeze("blocked", time.Minute)
	start := time.Now()
	if err := controller.Wait(context.Background(), "free"); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatal("unrelated key was blocked")
	}
}

func TestWaitHonorsContextCancellation(t *testing.T) {
	controller := NewController()
	controller.Freeze("k", time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := controller.Wait(ctx, "k"); err == nil {
		t.Fatal("expected context error")
	}
}
