// Package cache keeps outside services' answers (TMDB pages and details,
// Seerr's live status, MDBList answers, the search AI's guesses) in Valkey,
// shared by every API replica, with a time to live on every key.
//
// What it does better than the Python's in-process dict:
//
//   - Shared: two replicas (or a restart) no longer each ask TMDB for the
//     same page. One upstream call per key per window, install-wide.
//   - Bounded: every key has a TTL in Valkey, so nothing grows without end
//     (the Python kept every expired entry in memory until asked again).
//   - Keys holding text a person typed (a search) are hashed (Key), so the
//     cache never holds what someone searched for in the clear and a long
//     text cannot make a long key.
//   - One call per key at a time within a replica (singleflight), so a cold
//     page with 40 cards does not fire 40 identical calls.
//
// A kept copy ("stale" value) is what lets a TMDB list keep showing while
// TMDB is down: each entry is stored with the time it was fetched, and
// Valkey keeps it for Keep (longer than the TTL). Fresh within TTL, it is
// served as is; past TTL it is fetched again, and only if that fetch fails
// does CachedOrCopy hand back the kept copy (the deliberate degrade the
// Python had, rows.tmdb_list).
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/valkey-io/valkey-go"
	"golang.org/x/sync/singleflight"
)

// prefix keeps the cache's keys apart from the other Valkey users (the
// rate limits, the breakers, the OIDC state).
const prefix = "c:"

// Cache is the shared cache.
type Cache struct {
	kv     valkey.Client
	flight singleflight.Group
	log    *slog.Logger
	now    func() time.Time
}

// New makes the cache on the shared Valkey client.
func New(kv valkey.Client, log *slog.Logger) *Cache {
	return &Cache{kv: kv, log: log, now: time.Now}
}

// Key joins parts into a cache key, hashing the last one when hash is
// true (a search text: never kept in the clear, never longer than 64
// characters). The text is lowercased and trimmed first, so "Alien " and
// "alien" share an entry.
func Key(hash bool, parts ...string) string {
	if hash && len(parts) > 0 {
		last := strings.ToLower(strings.TrimSpace(parts[len(parts)-1]))
		sum := sha256.Sum256([]byte(last))
		parts[len(parts)-1] = hex.EncodeToString(sum[:])
	}
	return strings.Join(parts, ":")
}

// entry is what Valkey holds: the value and until when it is fresh (unix
// milliseconds). The freshness rides in the entry, so an answer that picked
// its own (TTLFor) is read back with it; Valkey's own expiry is the end of
// the kept copy.
type entry struct {
	Until int64           `json:"u"`
	V     json.RawMessage `json:"v"`
}

// Options say how long an answer is fresh, and how long a copy is kept
// past that for CachedOrCopy (0: no copy beyond TTL).
type Options struct {
	TTL  time.Duration
	Keep time.Duration
	// TTLFor, when set, picks the freshness from the answer itself (the
	// search AI keeps a real answer a day and an empty one a minute).
	TTLFor func(raw []byte) time.Duration
}

// ErrMiss is Peek finding nothing.
var ErrMiss = errors.New("cache: miss")

// Cached returns the fresh value under key, or calls fn, stores its answer
// and returns it. An error from fn is returned as is (never cached).
func Cached[T any](ctx context.Context, c *Cache, key string, o Options, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	raw, fresh, err := c.read(ctx, key)
	if err == nil && fresh {
		var v T
		if json.Unmarshal(raw, &v) == nil {
			return v, nil
		}
	}
	got, err := c.fill(ctx, key, o, func(ctx context.Context) (any, error) { return fn(ctx) })
	if err != nil {
		return zero, err
	}
	return got.(T), nil
}

// CachedOrCopy is Cached, but when fn fails and a copy past its TTL is
// still kept (Options.Keep), that copy is returned with stale true and the
// error logged; only with no copy at all is the error returned.
func CachedOrCopy[T any](ctx context.Context, c *Cache, key string, o Options, fn func(context.Context) (T, error)) (v T, stale bool, err error) {
	raw, fresh, rerr := c.read(ctx, key)
	if rerr == nil && fresh {
		if json.Unmarshal(raw, &v) == nil {
			return v, false, nil
		}
	}
	got, err := c.fill(ctx, key, o, func(ctx context.Context) (any, error) { return fn(ctx) })
	if err == nil {
		return got.(T), false, nil
	}
	if rerr == nil {
		var old T
		if json.Unmarshal(raw, &old) == nil {
			c.log.WarnContext(ctx, "upstream failed; serving the kept copy", "key", key, "err", err)
			return old, true, nil
		}
	}
	return v, false, err
}

// fill runs fn once per key in this replica at a time and stores a good
// answer. Valkey trouble while storing is logged, not returned: the answer
// itself is good, the next caller will simply fetch again.
func (c *Cache) fill(ctx context.Context, key string, o Options, fn func(context.Context) (any, error)) (any, error) {
	got, err, _ := c.flight.Do(key, func() (any, error) {
		// Detached from the first caller's cancellation: the others waiting
		// on this key would otherwise fail because one browser tab closed.
		// Every upstream call has its own deadline (package clients).
		ctx := context.WithoutCancel(ctx)
		v, err := fn(ctx)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		c.store(ctx, key, raw, o)
		return v, nil
	})
	return got, err
}

func (c *Cache) store(ctx context.Context, key string, raw []byte, o Options) {
	ttl := o.TTL
	if o.TTLFor != nil {
		ttl = o.TTLFor(raw)
	}
	life := ttl + o.Keep
	if life <= 0 {
		return
	}
	body, _ := json.Marshal(entry{Until: c.now().Add(ttl).UnixMilli(), V: raw})
	cmd := c.kv.B().Set().Key(prefix + key).Value(valkey.BinaryString(body)).Px(life).Build()
	if err := c.kv.Do(ctx, cmd).Error(); err != nil {
		c.log.WarnContext(ctx, "cache write failed", "err", err)
	}
}

// read returns the stored value and whether it is still fresh. A Valkey
// failure reads as a miss (logged): the caller then asks the service
// itself, which is the right thing for a cache.
func (c *Cache) read(ctx context.Context, key string) ([]byte, bool, error) {
	raw, err := c.kv.Do(ctx, c.kv.B().Get().Key(prefix+key).Build()).AsBytes()
	if err != nil {
		if !valkey.IsValkeyNil(err) {
			c.log.WarnContext(ctx, "cache read failed", "err", err)
		}
		return nil, false, ErrMiss
	}
	var e entry
	if json.Unmarshal(raw, &e) != nil {
		return nil, false, ErrMiss
	}
	return e.V, c.now().UnixMilli() < e.Until, nil
}

// Forget drops keys (a request made here makes Seerr's status stale at
// once; a ticked episode makes Up next stale).
func (c *Cache) Forget(ctx context.Context, keys ...string) {
	if len(keys) == 0 {
		return
	}
	full := make([]string, 0, len(keys))
	for _, k := range keys {
		full = append(full, prefix+k)
	}
	if err := c.kv.Do(ctx, c.kv.B().Del().Key(full...).Build()).Error(); err != nil {
		c.log.WarnContext(ctx, "cache forget failed", "err", err)
	}
}

// ForgetPrefix drops every key under p (after a library sync, every
// Jellyfin answer: Up next, episode lists, the server id). Keys are found
// with SCAN in pages (never KEYS, which blocks Valkey while it walks the
// whole keyspace) and removed with UNLINK, which frees them off Valkey's
// main thread. A failure is logged: the entries then age out on their own
// short TTLs.
func (c *Cache) ForgetPrefix(ctx context.Context, p string) {
	var cursor uint64
	for {
		e, err := c.kv.Do(ctx, c.kv.B().Scan().Cursor(cursor).Match(prefix+p+"*").Count(500).Build()).AsScanEntry()
		if err != nil {
			c.log.WarnContext(ctx, "cache forget failed", "prefix", p, "err", err)
			return
		}
		if len(e.Elements) > 0 {
			if err := c.kv.Do(ctx, c.kv.B().Unlink().Key(e.Elements...).Build()).Error(); err != nil {
				c.log.WarnContext(ctx, "cache forget failed", "prefix", p, "err", err)
				return
			}
		}
		if cursor = e.Cursor; cursor == 0 {
			return
		}
	}
}

// Keys more than one package reads or forgets. Defined once here so a
// writer and its readers can never drift apart.

// JellyfinPrefix is every cached Jellyfin answer: forgotten as a whole when
// the library changes (the Python's cache.invalidate("jf:")).
const JellyfinPrefix = "jf:"

// ContinueKey is a person's Home "Up next" answer (Jellyfin's resume and
// next-up lists). Everything that changes what they have watched forgets
// it: a ticked episode, "Finished it?", a plays sync that added plays, the
// Jellyfin plugin's events.
func ContinueKey(userID int32) string {
	return JellyfinPrefix + "continue:" + strconv.Itoa(int(userID))
}

// Valkey is the client, for packages that keep their own structures next
// to the cache (the Seerr status hash in package userstate).
func (c *Cache) Valkey() valkey.Client { return c.kv }
