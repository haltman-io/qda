// Package ratelimit implements keyed request pacing with freezes.
//
// Every upstream (RDAP registry, Cloudflare account, Vercel account, ...)
// gets its own key. A key enforces a minimum interval between requests and
// can be frozen for a period when the upstream answers with a rate limit or
// a permission block. Frozen keys make Wait sleep until the freeze expires,
// while other keys keep flowing.
package ratelimit

import (
	"context"
	"sync"
	"time"

	"qda/internal/httpkit"
)

// Controller paces requests per key.
type Controller struct {
	mu   sync.Mutex
	keys map[string]*keyState
}

type keyState struct {
	minInterval time.Duration
	nextAllowed time.Time
	frozenUntil time.Time
}

// NewController creates an empty controller.
func NewController() *Controller {
	return &Controller{keys: map[string]*keyState{}}
}

// SetInterval configures the minimum interval between requests for a key.
func (c *Controller) SetInterval(key string, interval time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state(key).minInterval = interval
}

// Freeze blocks a key for the given duration. Later freezes extend an
// active freeze but never shorten it.
func (c *Controller) Freeze(key string, duration time.Duration) {
	if duration < 0 {
		duration = 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.state(key)
	until := time.Now().Add(duration)
	if until.After(state.frozenUntil) {
		state.frozenUntil = until
	}
}

// FrozenUntil reports when the key becomes usable again (zero when usable).
func (c *Controller) FrozenUntil(key string) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	state, ok := c.keys[key]
	if !ok {
		return time.Time{}
	}
	return state.frozenUntil
}

// Frozen reports whether the key is currently frozen.
func (c *Controller) Frozen(key string) bool {
	return time.Now().Before(c.FrozenUntil(key))
}

// Wait blocks until the key is allowed to fire, then reserves the next
// slot. It honors context cancellation while sleeping.
func (c *Controller) Wait(ctx context.Context, key string) error {
	for {
		c.mu.Lock()
		state := c.state(key)
		now := time.Now()
		wait := time.Duration(0)
		if state.frozenUntil.After(now) {
			wait = state.frozenUntil.Sub(now)
		}
		if state.nextAllowed.After(now.Add(wait)) {
			wait = state.nextAllowed.Sub(now)
		}
		if wait <= 0 {
			state.nextAllowed = now.Add(state.minInterval)
			c.mu.Unlock()
			return nil
		}
		c.mu.Unlock()

		if err := httpkit.SleepContext(ctx, wait); err != nil {
			return err
		}
	}
}

func (c *Controller) state(key string) *keyState {
	state, ok := c.keys[key]
	if !ok {
		state = &keyState{}
		c.keys[key] = state
	}
	return state
}
