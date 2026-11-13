package clients

import (
	"context"
	"log/slog"
	"time"

	"github.com/valkey-io/valkey-go"
)

// Breaker is a circuit breaker shared by every replica through Valkey: when
// a service fails or refuses, it is left alone for a while, so nothing waits
// on it again meanwhile. The Python kept this per process (app/clients/
// mdblist.py, _down_until), so each new replica would have hammered the
// service again.
//
// Valkey being unreachable is not a reason to stop calling the service: the
// breaker then reports "closed" and logs it. That is the one deliberate
// degrade here (the breaker is an optimisation; the call itself still
// reports its own errors).
type Breaker struct {
	kv   valkey.Client
	key  string
	log  *slog.Logger
	name string
}

// NewBreaker makes the breaker for one service.
func NewBreaker(kv valkey.Client, name string, log *slog.Logger) *Breaker {
	return &Breaker{kv: kv, key: "breaker:" + name, log: log, name: name}
}

// Open trips the breaker for d, remembering why.
func (b *Breaker) Open(ctx context.Context, why string, d time.Duration) {
	err := b.kv.Do(ctx, b.kv.B().Set().Key(b.key).Value(why).Px(d).Build()).Error()
	if err != nil {
		b.log.WarnContext(ctx, "could not record a circuit breaker", "service", b.name, "err", err)
	}
	b.log.WarnContext(ctx, "outside service left alone for a while", "service", b.name, "why", why, "for_s", int(d.Seconds()))
}

// IsOpen reports whether the service is being left alone, and why.
func (b *Breaker) IsOpen(ctx context.Context) (bool, string) {
	why, err := b.kv.Do(ctx, b.kv.B().Get().Key(b.key).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return false, ""
	}
	if err != nil {
		b.log.WarnContext(ctx, "circuit breaker unreadable; calling the service anyway", "service", b.name, "err", err)
		return false, ""
	}
	return true, why
}
