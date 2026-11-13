// Package tasks is a small delayed work queue in Valkey, shared by every API
// replica and every worker: the API asks for work (sync this person's
// Jellyfin plays, rebuild their AI rows) and a worker does it.
//
// Why this exists: the Python started such work with asyncio.create_task in
// the web process and kept "already asked" in a dict in that process's
// memory. With two replicas each kept its own dict (two syncs, two AI calls
// for one person), the tasks died with a restart, and one create_task
// (episodes.py) was not even referenced, so Python could drop it mid-run.
// Here no API request starts background work at all; it only records it.
//
// The shape:
//
//   - a sorted set "task:<queue>" of members (a person's id) scored by when
//     the work is due, in milliseconds;
//   - a "pending" marker per member ("task:<queue>:pending:<member>") set
//     together with the entry, atomically, so asking again while the work is
//     waiting (or running, for queues that keep the marker until done) adds
//     nothing: that is the debounce and the dedupe;
//   - Due pops the members whose time has come, atomically, so each piece of
//     work goes to exactly one worker however many poll.
//
// What it does not promise: a worker that dies after popping a member loses
// that one run. Every queue here is a speed-up over a scheduled job that
// catches up (the 5-minute plays sync; the AI rows are asked for again on
// the person's next Home visit once the pending marker expires), so a lost
// run is late, never lost for good. That trade keeps this to two Valkey
// commands instead of a broker.
package tasks

import (
	"context"
	"strconv"
	"time"

	"github.com/valkey-io/valkey-go"
)

// Queue is one kind of work.
type Queue struct {
	kv   valkey.Client
	name string
	now  func() time.Time
}

// New makes the queue "name".
func New(kv valkey.Client, name string) *Queue {
	return &Queue{kv: kv, name: name, now: time.Now}
}

func (q *Queue) setKey() string                  { return "task:" + q.name }
func (q *Queue) pendingKey(member string) string { return "task:" + q.name + ":pending:" + member }
func (q *Queue) lockKey(member string) string    { return "task:" + q.name + ":running:" + member }

// enqueueScript adds the member only when no pending marker exists, and sets
// the marker with its expiry in the same step.
// KEYS: set, pending. ARGV: due ms, member, hold ms. Returns 1 when added.
var enqueueScript = valkey.NewLuaScript(`
if redis.call('SET', KEYS[2], '1', 'NX', 'PX', ARGV[3]) then
  redis.call('ZADD', KEYS[1], ARGV[1], ARGV[2])
  return 1
end
return 0`)

// Enqueue asks for the member's work in delay, unless it was asked for and
// not yet released (Release): then nothing changes and added is false. The
// pending marker lasts hold, the longest the work can take, so a worker that
// died mid-run never blocks the member for longer than that.
func (q *Queue) Enqueue(ctx context.Context, member string, delay, hold time.Duration) (added bool, err error) {
	due := q.now().Add(delay).UnixMilli()
	n, err := enqueueScript.Exec(ctx, q.kv, []string{q.setKey(), q.pendingKey(member)},
		[]string{strconv.FormatInt(due, 10), member, strconv.FormatInt(hold.Milliseconds(), 10)}).AsInt64()
	return n == 1, err
}

// dueScript pops up to ARGV[2] members due by ARGV[1].
var dueScript = valkey.NewLuaScript(`
local items = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1], 'LIMIT', 0, tonumber(ARGV[2]))
if #items > 0 then redis.call('ZREM', KEYS[1], unpack(items)) end
return items`)

// Due takes up to n members whose work is due; no other caller gets them.
func (q *Queue) Due(ctx context.Context, n int) ([]string, error) {
	return dueScript.Exec(ctx, q.kv, []string{q.setKey()},
		[]string{strconv.FormatInt(q.now().UnixMilli(), 10), strconv.Itoa(n)}).AsStrSlice()
}

// Release drops the member's pending marker, so the next Enqueue adds it
// again. A queue that debounces events releases when it starts the work
// (events during the run ask for another run after it); a queue that
// dedupes expensive work releases when it is done.
func (q *Queue) Release(ctx context.Context, member string) error {
	return q.kv.Do(ctx, q.kv.B().Del().Key(q.pendingKey(member)).Build()).Error()
}

// Pending reports whether the member's work is asked for and not released.
func (q *Queue) Pending(ctx context.Context, member string) (bool, error) {
	n, err := q.kv.Do(ctx, q.kv.B().Exists().Key(q.pendingKey(member)).Build()).AsInt64()
	return n > 0, err
}

// Lock takes the member's run lock for at most ttl: two workers must never
// run one member's work at once (two plays syncs of one person would both
// add the same new play). false: another run holds it.
func (q *Queue) Lock(ctx context.Context, member string, ttl time.Duration) (bool, error) {
	err := q.kv.Do(ctx, q.kv.B().Set().Key(q.lockKey(member)).Value("1").Nx().Px(ttl).Build()).Error()
	if valkey.IsValkeyNil(err) {
		return false, nil
	}
	return err == nil, err
}

// Unlock frees the member's run lock.
func (q *Queue) Unlock(ctx context.Context, member string) error {
	return q.kv.Do(ctx, q.kv.B().Del().Key(q.lockKey(member)).Build()).Error()
}

// Retry puts a popped member back, due in delay (its lock was held by
// another run). The pending marker is left as it is.
func (q *Queue) Retry(ctx context.Context, member string, delay time.Duration) error {
	due := q.now().Add(delay).UnixMilli()
	return q.kv.Do(ctx, q.kv.B().Zadd().Key(q.setKey()).ScoreMember().ScoreMember(float64(due), member).Build()).Error()
}
