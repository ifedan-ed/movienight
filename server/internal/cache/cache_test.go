package cache_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"movienight/server/internal/cache"
	"movienight/server/internal/testbase"
)

func newCache(t *testing.T) *cache.Cache {
	_, kv := testbase.Conns(t)
	return cache.New(kv, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestOneCallPerKeyEvenWhenManyAskAtOnce(t *testing.T) {
	c := newCache(t)
	var calls atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := cache.Cached(context.Background(), c, "k", cache.Options{TTL: time.Minute}, func(context.Context) (int, error) {
				calls.Add(1)
				time.Sleep(50 * time.Millisecond)
				return 42, nil
			})
			if err != nil || v != 42 {
				t.Error(v, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("%d calls", calls.Load())
	}
}

func TestAFailureServesTheKeptCopyOnlyWhenThereIsOne(t *testing.T) {
	c := newCache(t)
	ctx := context.Background()
	o := cache.Options{TTL: 10 * time.Millisecond, Keep: time.Minute}
	boom := errors.New("TMDB down")
	if _, _, err := cache.CachedOrCopy(ctx, c, "page", o, func(context.Context) ([]int, error) { return nil, boom }); err != boom {
		t.Fatal("no copy, but no error", err)
	}
	_, _, _ = cache.CachedOrCopy(ctx, c, "page", o, func(context.Context) ([]int, error) { return []int{1, 2}, nil })
	time.Sleep(20 * time.Millisecond)
	got, stale, err := cache.CachedOrCopy(ctx, c, "page", o, func(context.Context) ([]int, error) { return nil, boom })
	if err != nil || !stale || len(got) != 2 {
		t.Fatal(got, stale, err)
	}
	// Plain Cached never serves a stale copy.
	if _, err := cache.Cached(ctx, c, "page", o, func(context.Context) ([]int, error) { return nil, boom }); err != boom {
		t.Fatal(err)
	}
}

func TestSearchTextIsNeverKeptInTheClear(t *testing.T) {
	k := cache.Key(true, "search", "1", "  Kids with a Treasure MAP ")
	if strings.Contains(strings.ToLower(k), "treasure") || k != cache.Key(true, "search", "1", "kids with a treasure map") {
		t.Fatal(k)
	}
}
