package passwords

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHashAndCheck(t *testing.T) {
	h := NewHasher()
	ctx := context.Background()
	stored, err := h.Hash(ctx, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := h.Check(ctx, "correct horse battery", &stored); !ok {
		t.Fatal("did not match")
	}
	if ok, _ := h.Check(ctx, "correct horse batterY", &stored); ok {
		t.Fatal("a wrong password matched")
	}
	// No stored hash: checked against the dummy (same cost), never a match.
	if ok, _ := h.Check(ctx, "", nil); ok {
		t.Fatal("no hash matched")
	}
}

func TestThePoolBoundsConcurrentHashing(t *testing.T) {
	h := NewHasher()
	var running, peak atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 3*Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := h.acquire(context.Background()); err != nil {
				t.Error(err)
				return
			}
			n := running.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			running.Add(-1)
			h.release()
		}()
	}
	wg.Wait()
	if peak.Load() > Workers {
		t.Fatalf("%d ran at once", peak.Load())
	}
	// A caller whose request is gone stops waiting.
	for i := 0; i < Workers; i++ {
		_ = h.acquire(context.Background())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := h.acquire(ctx); err == nil {
		t.Fatal("acquired past the bound")
	}
}

func TestValidate(t *testing.T) {
	s := &Service{minLength: 10}
	if s.Validate("short") == nil || s.Validate(string(make([]rune, 201))) == nil || s.Validate("long enough pw") != nil {
		t.Fatal("validation")
	}
}
