// Package imdb keeps IMDb's own ratings here (app/services/imdb.py). IMDb
// publishes every rated title's rating and vote count as a free file,
// refreshed daily (datasets.imdbws.com, title.ratings.tsv.gz, about 9 MB
// gzipped, 1.5 million lines; for personal, non-commercial use). The
// worker's nightly job loads all of it into imdb_ratings, and a title page
// reads one rating by its IMDb id, so no outside service is asked when a
// page opens.
//
// A load fills a new table and swaps it in at the end, in one transaction:
// readers see yesterday's ratings until today's are complete, and a broken
// or cut-short download changes nothing.
//
// Memory stays bounded whatever the file's size: the download is
// decompressed as it arrives and handed to Postgres's COPY a buffer at a
// time; no part of the file is ever held whole (the Python's web process
// peaked at 756 MB once, with this kind of work running inside it).
package imdb

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"movienight/server/internal/store"
)

// URL is IMDb's ratings file.
const URL = "https://datasets.imdbws.com/title.ratings.tsv.gz"

// Header is the file's first line: anything else is not IMDb's ratings file.
const Header = "tconst\taverageRating\tnumVotes"

// KeepShare: a new file must hold at least this share of the titles already
// loaded, so a half-empty file (IMDb trouble, a proxy's error page that
// happens to be gzip) never replaces a good one.
const KeepShare = 0.9

// downloadTimeout bounds the whole download and load (the Python allowed
// 120 s between reads; a whole-request deadline is simpler and also stops a
// server that trickles bytes forever).
const downloadTimeout = 10 * time.Minute

// loadLock is the advisory lock id of a load ("mnimdb"): the worker runs the
// job once per due time already (job_runs), and this also keeps an
// operator's manual run from racing the nightly one on the same tables.
const loadLock int64 = 0x6d6e696d6462

// FileError is a download that is not a whole, plausible IMDb ratings file.
// The ratings already loaded stay.
type FileError struct{ Msg string }

func (e *FileError) Error() string { return "IMDb ratings file: " + e.Msg }

// Service loads and reads the ratings.
type Service struct {
	pool *pgxpool.Pool
	http *http.Client
	url  string
	q    *store.Queries
}

// New makes the service; url is URL outside tests.
func New(pool *pgxpool.Pool, client *http.Client, url string) *Service {
	return &Service{pool: pool, http: client, url: url, q: store.New(pool)}
}

// Rating is one title's IMDb rating.
type Rating struct {
	Rating float64 `json:"imdb_rating"`
	Votes  int32   `json:"imdb_votes"`
}

// Rating is the rating of an IMDb id (tt...); ok false when IMDb has none.
func (s *Service) Rating(ctx context.Context, imdbID string) (Rating, bool, error) {
	if imdbID == "" {
		return Rating{}, false, nil
	}
	r, err := s.q.ImdbRatingGet(ctx, imdbID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Rating{}, false, nil
	}
	if err != nil {
		return Rating{}, false, err
	}
	return Rating{Rating: r.Rating, Votes: r.Votes}, true, nil
}

// Loaded reports whether any ratings are loaded (the worker loads them soon
// after its first start when none are).
func (s *Service) Loaded(ctx context.Context) (bool, error) { return s.q.ImdbLoaded(ctx) }

// Refresh downloads today's file and loads it; returns how many titles.
func (s *Service) Refresh(ctx context.Context) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("IMDb's ratings file couldn't be downloaded: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("IMDb answered HTTP %d for its ratings file", resp.StatusCode)
	}
	return s.Load(ctx, resp.Body)
}

// Load replaces imdb_ratings with the gzipped TSV read from r. Returns how
// many titles were loaded; a *FileError (keeping the old ratings) when the
// file is not what IMDb sends.
//
// Plain pgx, not sqlc: COPY FROM a stream and the table swap are not
// queries sqlc can describe.
func (s *Service) Load(ctx context.Context, r io.Reader) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	// Rolled back unless committed below: every failure leaves the old
	// table exactly as it was (and drops the half-filled new one).
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", loadLock); err != nil {
		return 0, err
	}
	var before int64
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM imdb_ratings").Scan(&before); err != nil {
		return 0, err
	}
	for _, stmt := range []string{
		"DROP TABLE IF EXISTS imdb_ratings_load",
		"CREATE TABLE imdb_ratings_load (LIKE imdb_ratings INCLUDING DEFAULTS)",
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return 0, err
		}
	}

	body, err := newBody(r)
	if err != nil {
		return 0, err
	}
	// The file's lines are already COPY's text format (tab-separated, one
	// row a line), so they go to Postgres as they are: no parsing in Go, and
	// Postgres checks every value's type.
	if _, err := tx.Conn().PgConn().CopyFrom(ctx, body,
		"COPY imdb_ratings_load (tconst, rating, votes) FROM STDIN"); err != nil {
		if body.fileErr != nil {
			return 0, body.fileErr
		}
		return 0, fmt.Errorf("loading IMDb's ratings: %w", err)
	}
	if body.fileErr != nil {
		return 0, body.fileErr
	}

	var after int64
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM imdb_ratings_load").Scan(&after); err != nil {
		return 0, err
	}
	if after == 0 || float64(after) < float64(before)*KeepShare {
		return 0, &FileError{Msg: fmt.Sprintf("the new file has %d titles against %d loaded", after, before)}
	}
	for _, stmt := range []string{
		"ALTER TABLE imdb_ratings_load ADD CONSTRAINT imdb_ratings_load_pkey PRIMARY KEY (tconst)",
		"DROP TABLE imdb_ratings",
		"ALTER TABLE imdb_ratings_load RENAME TO imdb_ratings",
		"ALTER TABLE imdb_ratings RENAME CONSTRAINT imdb_ratings_load_pkey TO imdb_ratings_pkey",
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int(after), nil
}

// body is the decompressed file minus its header line, as COPY reads it. It
// checks the header before handing over any data, and turns a cut-short or
// corrupt gzip stream into a *FileError (kept in fileErr, since COPY wraps
// the reader's error in its own).
type body struct {
	gz      *gzip.Reader
	br      *bufio.Reader
	fileErr error
}

func newBody(r io.Reader) (*body, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, &FileError{Msg: "the download was empty"}
		}
		return nil, &FileError{Msg: "not a gzip file (" + err.Error() + ")"}
	}
	b := &body{gz: gz, br: bufio.NewReaderSize(gz, 64<<10)}
	first, err := b.br.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, b.wrap(err)
	}
	if first == "" {
		return nil, &FileError{Msg: "the file was empty"}
	}
	line := string(bytes.TrimRight([]byte(first), "\r\n"))
	if line != Header {
		if len(line) > 60 {
			line = line[:60]
		}
		return nil, &FileError{Msg: fmt.Sprintf("not IMDb's ratings file (first line %q)", line)}
	}
	return b, nil
}

func (b *body) wrap(err error) error {
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return &FileError{Msg: "the download was cut short"}
	}
	return &FileError{Msg: "the download is corrupt (" + err.Error() + ")"}
}

// Read hands COPY the next decompressed bytes. gzip.Reader checks the
// stream's length and CRC at its end, so a file cut short or altered fails
// here rather than loading part of it.
func (b *body) Read(p []byte) (int, error) {
	n, err := b.br.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		b.fileErr = b.wrap(err)
		return n, b.fileErr
	}
	return n, err
}
