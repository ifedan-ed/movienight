package jfsync

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/valkey-io/valkey-go"

	"movienight/server/internal/cache"
)

// Episodes ticked or unticked in Movie Night are marked played or unplayed
// in the person's Jellyfin too, so Jellyfin's Next Up agrees. That is a
// handful of Jellyfin calls the person must not wait for, so the API only
// records them here and a worker makes them (RunQueued).
//
// The Python fired them with asyncio.create_task and kept no reference
// (episodes.py:180), so the task could be dropped mid-run, and a restart
// lost it. Here the marks wait in Valkey:
//
//   - "task:jf-marks:items:<user id>", a hash of Jellyfin item id -> the
//     latest instruction for it ("1:<unix ms>" played at that time, or "0"
//     unplayed). A tick and an untick in quick succession leave only the
//     last, so Jellyfin ends where Movie Night is, with no wasted call;
//   - the person's id in the "jf-marks" queue (tasks.Queue), deduped while
//     waiting.
//
// A worker releases the person, then takes the whole hash atomically and
// makes the calls: marks asked for meanwhile go into a fresh hash and a
// fresh run. A failed call is logged, not retried: the history here is
// already right, and Jellyfin's own Next Up is a courtesy.
const (
	MarksQueue = "jf-marks"
	// marksHold: the longest a request waits for a worker before it may be
	// asked for again (a worker that died after taking it).
	marksHold = 2 * time.Minute
	// marksKeep: how long waiting marks are kept when no worker runs at
	// all, so the hash never outlives its use.
	marksKeep = 24 * time.Hour
)

func marksKey(member string) string { return "task:" + MarksQueue + ":items:" + member }

// AskMarks records that items should be marked played (at when) or
// unplayed in the person's Jellyfin, for a worker to do. Called by the API.
func (s *Service) AskMarks(ctx context.Context, userID int32, items []string, played bool, when time.Time) error {
	if len(items) == 0 {
		return nil
	}
	value := "0"
	if played {
		value = "1:" + strconv.FormatInt(when.UnixMilli(), 10)
	}
	member := UserIDString(userID)
	set := s.kv.B().Hset().Key(marksKey(member)).FieldValue()
	for _, id := range items {
		set = set.FieldValue(id, value)
	}
	for _, r := range s.kv.DoMulti(ctx, set.Build(), s.kv.B().Pexpire().Key(marksKey(member)).Milliseconds(marksKeep.Milliseconds()).Build()) {
		if err := r.Error(); err != nil {
			return err
		}
	}
	_, err := s.marks.Enqueue(ctx, member, 0, marksHold)
	return err
}

// takeScript reads a person's waiting marks and deletes them in one step,
// so a mark written meanwhile is either in this run or in the next one.
var takeScript = valkey.NewLuaScript(`
local v = redis.call('HGETALL', KEYS[1])
redis.call('DEL', KEYS[1])
return v`)

func (s *Service) runMarks(ctx context.Context, member string) {
	if err := s.marks.Release(ctx, member); err != nil {
		s.log.ErrorContext(ctx, "Jellyfin marks queue: release failed", "err", err)
	}
	pairs, err := takeScript.Exec(ctx, s.kv, []string{marksKey(member)}, nil).AsStrSlice()
	if err != nil {
		s.log.ErrorContext(ctx, "Jellyfin marks unreadable", "err", err)
		return
	}
	n, err := strconv.Atoi(member)
	if err != nil {
		s.log.ErrorContext(ctx, "Jellyfin marks queue: not a person id; dropped", "member", member)
		return
	}
	id := int32(n)
	u, err := s.q.GetUserByID(ctx, id)
	if err != nil || u.JellyfinUserID == nil || *u.JellyfinUserID == "" {
		// Deleted or disconnected since: nothing to mark.
		return
	}
	jf, failed := s.clients.Jellyfin, 0
	for i := 0; i+1 < len(pairs); i += 2 {
		item, how := pairs[i], pairs[i+1]
		if ms, ok := strings.CutPrefix(how, "1:"); ok {
			at, _ := strconv.ParseInt(ms, 10, 64)
			err = jf.MarkPlayed(ctx, *u.JellyfinUserID, item, time.UnixMilli(at))
		} else {
			err = jf.MarkUnplayed(ctx, *u.JellyfinUserID, item)
		}
		if err != nil {
			failed++
		}
	}
	if failed > 0 {
		s.log.WarnContext(ctx, "Jellyfin played marks failed for some episodes", "user", id, "failed", failed, "of", len(pairs)/2)
	}
	// Up next follows Jellyfin's Next Up, which these marks just changed.
	s.cache.Forget(ctx, cache.ContinueKey(id))
}
