package imdb_test

// IMDb's own ratings: tests/test_outside_services.py (the IMDb part), plus
// a large streamed load that must stay within a small memory budget.

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"movienight/server/internal/imdb"
	"movienight/server/internal/testbase"
)

const ratingsTSV = "tconst\taverageRating\tnumVotes\ntt0000603\t7.9\t123456\ntt0001399\t9.2\t2400000\n"

func gz(t testing.TB, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// trickle hands the reader 7 bytes at a time, as a slow download would.
type trickle struct{ r io.Reader }

func (t trickle) Read(p []byte) (int, error) {
	if len(p) > 7 {
		p = p[:7]
	}
	return t.r.Read(p)
}

func ratings(t *testing.T, pool *pgxpool.Pool) map[string][2]float64 {
	t.Helper()
	rows, err := pool.Query(context.Background(), "SELECT tconst, rating, votes FROM imdb_ratings")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string][2]float64{}
	for rows.Next() {
		var id string
		var r float64
		var v int64
		if err := rows.Scan(&id, &r, &v); err != nil {
			t.Fatal(err)
		}
		out[id] = [2]float64{r, float64(v)}
	}
	return out
}

func TestTheFileLoadsAndReplacesTheOneBefore(t *testing.T) {
	pool, _ := testbase.Conns(t)
	ctx := context.Background()
	s := imdb.New(pool, http.DefaultClient, imdb.URL)
	if n, err := s.Load(ctx, trickle{bytes.NewReader(gz(t, ratingsTSV))}); err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	if got := ratings(t, pool)["tt0000603"]; got != [2]float64{7.9, 123456} {
		t.Fatal(got)
	}
	next := strings.Replace(ratingsTSV, "7.9\t123456", "8.0\t123999", 1) // the next night's file
	if n, err := s.Load(ctx, bytes.NewReader(gz(t, next))); err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	if got := ratings(t, pool)["tt0000603"]; got != [2]float64{8.0, 123999} {
		t.Fatal(got)
	}
	var pk string
	if err := pool.QueryRow(ctx, `SELECT conname FROM pg_constraint
		WHERE conrelid = 'imdb_ratings'::regclass AND contype = 'p'`).Scan(&pk); err != nil || pk != "imdb_ratings_pkey" {
		t.Fatalf("%q %v", pk, err)
	}
	var leftover *string
	if err := pool.QueryRow(ctx, "SELECT to_regclass('imdb_ratings_load')::text").Scan(&leftover); err != nil || leftover != nil {
		t.Fatalf("the load table was left behind: %v %v", leftover, err)
	}
	r, ok, err := s.Rating(ctx, "tt0001399")
	if err != nil || !ok || r.Rating != 9.2 || r.Votes != 2400000 {
		t.Fatal(r, ok, err)
	}
	if _, ok, _ := s.Rating(ctx, "tt9999999"); ok {
		t.Fatal("a title IMDb doesn't rate has no rating")
	}
}

func TestABrokenFileKeepsTheRatingsAlreadyHere(t *testing.T) {
	pool, _ := testbase.Conns(t)
	ctx := context.Background()
	s := imdb.New(pool, http.DefaultClient, imdb.URL)
	var many strings.Builder
	many.WriteString(imdb.Header + "\n")
	for n := 1; n <= 100; n++ {
		fmt.Fprintf(&many, "tt%07d\t7.0\t%d\n", n, n)
	}
	if _, err := s.Load(ctx, bytes.NewReader(gz(t, many.String()))); err != nil {
		t.Fatal(err)
	}
	full := gz(t, many.String())
	for name, data := range map[string][]byte{
		"not imdb's":       gz(t, "id\trating\tvotes\ntt1\t1\t1\n"),
		"cut short":        full[:len(full)-30],
		"far fewer titles": gz(t, ratingsTSV),
		"empty":            gz(t, ""),
		"not gzip":         []byte("<html>error page</html>"),
		"no download":      nil,
	} {
		_, err := s.Load(ctx, bytes.NewReader(data))
		var fe *imdb.FileError
		if !errors.As(err, &fe) {
			t.Fatalf("%s: %v", name, err)
		}
		if n := len(ratings(t, pool)); n != 100 {
			t.Fatalf("%s: %d ratings left", name, n)
		}
	}
}

func TestTheFileIsDownloadedFromIMDb(t *testing.T) {
	pool, _ := testbase.Conns(t)
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		_, _ = w.Write(gz(t, ratingsTSV))
	}))
	defer srv.Close()
	s := imdb.New(pool, srv.Client(), srv.URL+"/title.ratings.tsv.gz")
	ctx := context.Background()
	if ok, _ := s.Loaded(ctx); ok {
		t.Fatal("nothing is loaded yet")
	}
	if n, err := s.Refresh(ctx); err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	if ok, err := s.Loaded(ctx); !ok || err != nil || len(seen) != 1 || seen[0] != "/title.ratings.tsv.gz" {
		t.Fatal(ok, err, seen)
	}
	if imdb.URL != "https://datasets.imdbws.com/title.ratings.tsv.gz" {
		t.Fatal(imdb.URL)
	}
}

func TestAnErrorAnswerChangesNothing(t *testing.T) {
	pool, _ := testbase.Conns(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	if _, err := imdb.New(pool, srv.Client(), srv.URL).Refresh(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "503") {
		t.Fatal(err)
	}
}

// generated streams a gzipped ratings file of n lines, made as it is read,
// so the test itself never holds the file either.
func generated(n int) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		w := gzip.NewWriter(pw)
		_, _ = io.WriteString(w, imdb.Header+"\n")
		// One reused line buffer: the generator's own allocations must
		// not count against the load's.
		line := make([]byte, 0, 64)
		for i := 1; i <= n; i++ {
			line = append(line[:0], "tt"...)
			line = strconv.AppendInt(line, int64(10_000_000+i), 10)
			line = append(line, '\t', byte('0'+i%10), '.', byte('0'+i%7), '\t')
			line = strconv.AppendInt(line, int64(i*3), 10)
			line = append(line, '\n')
			_, _ = w.Write(line)
		}
		_ = w.Close()
		_ = pw.Close()
	}()
	return pr
}

// A file the size of IMDb's own (about 1.5 million lines, 25 MB
// decompressed; 400,000 here to keep the test quick) loads in bounded
// memory: the heap grows by well under the file's size.
func TestALargeFileStreamsInBoundedMemory(t *testing.T) {
	pool, _ := testbase.Conns(t)
	const lines = 400_000
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	n, err := imdb.New(pool, http.DefaultClient, imdb.URL).Load(context.Background(), generated(lines))
	if err != nil || n != lines {
		t.Fatalf("%d %v", n, err)
	}
	runtime.ReadMemStats(&after)
	// TotalAlloc counts every allocation over the load: a streamed load
	// allocates buffers it reuses, far below the ~10 MB decompressed file.
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 8<<20 {
		t.Fatalf("the load allocated %d MB for a %d-line file", grew>>20, lines)
	}
}
