// Package sources implements the availability sources: RDAP (authoritative,
// default) and the optional registrar fallbacks Cloudflare, Vercel and
// Hostinger with credential rotation.
package sources

import (
	"context"

	"qda/internal/config"
	"qda/internal/types"
)

// Source is a single-domain availability source.
type Source interface {
	// Name identifies the source (cloudflare, vercel, hostinger).
	Name() string
	// Enabled reports whether the source has usable credentials.
	Enabled() bool
	// Check queries one domain.
	Check(ctx context.Context, domain string) types.SourceResult
}

// Chain is the ordered registrar fallback chain.
type Chain struct {
	sources []Source
}

// NewChain builds the fallback chain from settings, skipping sources
// without credentials.
func NewChain(settings config.Settings) *Chain {
	chain := &Chain{}
	cloudflare := NewCloudflare(settings)
	if cloudflare.Enabled() {
		chain.sources = append(chain.sources, cloudflare)
	}
	vercel := NewVercel(settings)
	if vercel.Enabled() {
		chain.sources = append(chain.sources, vercel)
	}
	hostinger := NewHostinger(settings)
	if hostinger.Enabled() {
		chain.sources = append(chain.sources, hostinger)
	}
	return chain
}

// Sources returns the enabled sources in priority order.
func (c *Chain) Sources() []Source {
	return append([]Source(nil), c.sources...)
}

// Empty reports whether no fallback source is enabled.
func (c *Chain) Empty() bool {
	return len(c.sources) == 0
}
