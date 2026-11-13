package tasks_test

// The queue's promises against a real Valkey: asking twice while waiting
// adds one entry (debounce, dedupe); due work goes to exactly one of many
// pollers; a released member can be asked for again; the run lock is
// exclusive.

import (
	"context"
	"sync"
	"testing"
	"time"

	"movienight/server/internal/tasks"
	"movienight/server/internal/testbase"
)

func TestAskingTwiceWhileWaitingAddsOneEntry(t *testing.T) {
	_, kv := testbase.Conns(t)
	ctx := context.Background()
	q := tasks.New(kv, "t")
	for i, want := range []bool{true, false, false} {
		added, err := q.Enqueue(ctx, "7", 0, time.Minute)
		if err != nil || added != want {
			t.Fatalf("ask %d: added %v (%v), want %v", i, added, err, want)
		}
	}
	got, err := q.Due(ctx, 10)
	if err != nil || len(got) != 1 || got[0] != "7" {
		t.Fatalf("due %v %v", got, err)
	}
	// Still pending until released: a new ask joins nothing and adds nothing.
	if added, _ := q.Enqueue(ctx, "7", 0, time.Minute); added {
		t.Fatal("asked again before release")
	}
	if err := q.Release(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if added, _ := q.Enqueue(ctx, "7", 0, time.Minute); !added {
		t.Fatal("not asked again after release")
	}
}

func TestWorkIsNotDueBeforeItsTime(t *testing.T) {
	_, kv := testbase.Conns(t)
	ctx := context.Background()
	q := tasks.New(kv, "t")
	if _, err := q.Enqueue(ctx, "1", 300*time.Millisecond, time.Minute); err != nil {
		t.Fatal(err)
	}
	if got, _ := q.Due(ctx, 10); len(got) != 0 {
		t.Fatalf("due too soon: %v", got)
	}
	time.Sleep(350 * time.Millisecond)
	if got, _ := q.Due(ctx, 10); len(got) != 1 {
		t.Fatalf("not due after its delay: %v", got)
	}
}

func TestDueWorkGoesToExactlyOnePoller(t *testing.T) {
	_, kv := testbase.Conns(t)
	ctx := context.Background()
	q := tasks.New(kv, "t")
	const n = 200
	for i := 0; i < n; i++ {
		if _, err := q.Enqueue(ctx, string(rune('a'+i%26))+string(rune('0'+i/26)), 0, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]int{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				got, err := q.Due(ctx, 7)
				if err != nil {
					t.Error(err)
					return
				}
				if len(got) == 0 {
					return
				}
				mu.Lock()
				for _, m := range got {
					seen[m]++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != n {
		t.Fatalf("%d members popped, want %d", len(seen), n)
	}
	for m, c := range seen {
		if c != 1 {
			t.Fatalf("%s popped %d times", m, c)
		}
	}
}

func TestTheRunLockIsExclusiveAndExpires(t *testing.T) {
	_, kv := testbase.Conns(t)
	ctx := context.Background()
	q := tasks.New(kv, "t")
	if ok, err := q.Lock(ctx, "5", 200*time.Millisecond); !ok || err != nil {
		t.Fatalf("first lock %v %v", ok, err)
	}
	if ok, _ := q.Lock(ctx, "5", time.Second); ok {
		t.Fatal("a held lock was taken")
	}
	time.Sleep(250 * time.Millisecond)
	if ok, _ := q.Lock(ctx, "5", time.Second); !ok {
		t.Fatal("an expired lock (a crashed holder) was not freed")
	}
	if err := q.Unlock(ctx, "5"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := q.Lock(ctx, "5", time.Second); !ok {
		t.Fatal("not free after Unlock")
	}
}
