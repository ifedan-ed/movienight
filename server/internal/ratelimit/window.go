// Package ratelimit counts events per key over a sliding window in Valkey,
// so every API replica sees the same counts (the Python kept them in one
// process's memory, which a restart forgot and a second replica would not
// share).
//
// Each key is a sorted set of event times (milliseconds). Old entries are
// trimmed on every call, and the key expires one window after its last
// event, so nothing is kept longer than the window. Keys are sha256-hashed:
// a username or an address never appears in Valkey in the clear, and a key's
// length is bounded whatever the input.
package ratelimit

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/valkey-io/valkey-go"
)

// Window is one kind of limit: at most Limit events per key in Period.
type Window struct {
	kv     valkey.Client
	prefix string
	Limit  int
	Period time.Duration
	now    func() time.Time
}

// New makes a window. name prefixes its keys ("rl:<name>:").
func New(kv valkey.Client, name string, limit int, period time.Duration) *Window {
	return &Window{kv: kv, prefix: "rl:" + name + ":", Limit: limit, Period: period, now: time.Now}
}

func (w *Window) key(k string) string {
	sum := sha256.Sum256([]byte(k))
	return w.prefix + hex.EncodeToString(sum[:16])
}

// Each event needs a unique member: two events in the same millisecond must
// both count.
func member(now int64) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return strconv.FormatInt(now, 10) + ":" + hex.EncodeToString(b)
}

// countScript trims old events and returns how many remain.
var countScript = valkey.NewLuaScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
return redis.call('ZCARD', KEYS[1])`)

// addScript records one event and refreshes the key's expiry.
var addScript = valkey.NewLuaScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
redis.call('ZADD', KEYS[1], ARGV[2], ARGV[3])
redis.call('PEXPIRE', KEYS[1], ARGV[4])
return redis.call('ZCARD', KEYS[1])`)

// takeScript records an event only while under the limit, atomically: it
// returns 1 when the event was allowed (and recorded), 0 when refused.
var takeScript = valkey.NewLuaScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
if redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[5]) then return 0 end
redis.call('ZADD', KEYS[1], ARGV[2], ARGV[3])
redis.call('PEXPIRE', KEYS[1], ARGV[4])
return 1`)

func (w *Window) args() (cutoff, nowMs int64) {
	now := w.now().UnixMilli()
	return now - w.Period.Milliseconds(), now
}

// Count returns how many events key had in the window.
func (w *Window) Count(ctx context.Context, k string) (int, error) {
	cutoff, _ := w.args()
	n, err := countScript.Exec(ctx, w.kv, []string{w.key(k)}, []string{strconv.FormatInt(cutoff, 10)}).AsInt64()
	return int(n), err
}

// Blocked reports whether key has reached the limit.
func (w *Window) Blocked(ctx context.Context, k string) (bool, error) {
	n, err := w.Count(ctx, k)
	return n >= w.Limit, err
}

// Add records one event for key.
func (w *Window) Add(ctx context.Context, k string) error {
	cutoff, now := w.args()
	return addScript.Exec(ctx, w.kv, []string{w.key(k)}, []string{
		strconv.FormatInt(cutoff, 10), strconv.FormatInt(now, 10), member(now), strconv.FormatInt(w.Period.Milliseconds(), 10),
	}).Error()
}

// Take records one event for key if it is under the limit, and reports
// whether it was.
func (w *Window) Take(ctx context.Context, k string) (bool, error) {
	cutoff, now := w.args()
	n, err := takeScript.Exec(ctx, w.kv, []string{w.key(k)}, []string{
		strconv.FormatInt(cutoff, 10), strconv.FormatInt(now, 10), member(now),
		strconv.FormatInt(w.Period.Milliseconds(), 10), strconv.Itoa(w.Limit),
	}).AsInt64()
	return n == 1, err
}

// Reset forgets key's events (a correct password clears its wrong ones).
func (w *Window) Reset(ctx context.Context, k string) error {
	return w.kv.Do(ctx, w.kv.B().Del().Key(w.key(k)).Build()).Error()
}
