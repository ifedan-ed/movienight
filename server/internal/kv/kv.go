// Package kv opens the shared Valkey client. Valkey holds the state every
// replica must agree on that is not durable data: caches (always with a
// TTL), the sign-in lockout, the TV pairing limit, the outside services'
// circuit breakers and the OIDC sign-in's state. Losing it costs at most a
// few cold caches and a reset lockout; nothing a person keeps lives there
// (browser sessions are in Postgres: migration 0017).
package kv

import (
	"context"
	"fmt"
	"time"

	"github.com/valkey-io/valkey-go"
)

// Open connects and pings. url is VALKEY_URL (redis://, rediss://, valkey://
// or unix://).
func Open(ctx context.Context, url string) (valkey.Client, error) {
	opt, err := valkey.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("VALKEY_URL is not a valid Valkey address")
	}
	// Client-side caching (RESP3 tracking) is off: every key here is either
	// short-lived or a counter, and invalidation traffic would cost more than
	// it saves at this size.
	opt.DisableCache = true
	c, err := valkey.NewClient(opt)
	if err != nil {
		return nil, fmt.Errorf("valkey: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.Do(pingCtx, c.B().Ping().Build()).Error(); err != nil {
		c.Close()
		return nil, fmt.Errorf("valkey did not answer: %w", err)
	}
	return c, nil
}
