package ratelimit_test

import (
	"context"
	"testing"
	"time"

	"movienight/server/internal/ratelimit"
	"movienight/server/internal/testbase"
)

func TestTakeCountsWithinTheWindowAndForgets(t *testing.T) {
	_, kv := testbase.Conns(t)
	ctx := context.Background()
	w := ratelimit.New(kv, "test", 3, 300*time.Millisecond)
	for i := 0; i < 3; i++ {
		if ok, err := w.Take(ctx, "a"); !ok || err != nil {
			t.Fatal(i, ok, err)
		}
	}
	if ok, _ := w.Take(ctx, "a"); ok {
		t.Fatal("a fourth was allowed")
	}
	if ok, _ := w.Take(ctx, "b"); !ok {
		t.Fatal("another key was blocked")
	}
	time.Sleep(350 * time.Millisecond)
	if ok, _ := w.Take(ctx, "a"); !ok {
		t.Fatal("the window did not pass")
	}
}

func TestAddBlockedAndReset(t *testing.T) {
	_, kv := testbase.Conns(t)
	ctx := context.Background()
	w := ratelimit.New(kv, "test2", 2, time.Minute)
	_ = w.Add(ctx, "k")
	if b, _ := w.Blocked(ctx, "k"); b {
		t.Fatal("blocked after one")
	}
	_ = w.Add(ctx, "k")
	if b, _ := w.Blocked(ctx, "k"); !b {
		t.Fatal("not blocked after two")
	}
	// Keys expire on their own, one window after the last event, and hold
	// no username or address in the clear (hashed).
	keys, _ := kv.Do(ctx, kv.B().Keys().Pattern("rl:test2:*").Build()).AsStrSlice()
	if len(keys) != 1 {
		t.Fatal(keys)
	}
	ttl, _ := kv.Do(ctx, kv.B().Pttl().Key(keys[0]).Build()).AsInt64()
	if ttl <= 0 || ttl > time.Minute.Milliseconds() {
		t.Fatal("ttl", ttl)
	}
	_ = w.Reset(ctx, "k")
	if n, _ := w.Count(ctx, "k"); n != 0 {
		t.Fatal(n)
	}
}
