// Package airows is "Picked for you": themed rows of films the install's
// model picks from a person's history, ratings and comments, matched on
// TMDB and kept in ai_row_cache.
//
// Who does what:
//
//   - the API (Home) calls Serve: it answers the cached rows at once and,
//     when they are stale, asks for new ones (Request). It never calls the
//     model itself;
//   - a worker (RunQueued) takes the request and builds the rows.
//
// The Python started the rebuild with asyncio.create_task in the web
// process and kept "being made" and "failed, wait" in that process's
// memory: two replicas made two model calls for one person, and a restart
// forgot a failure's back-off and lost a rebuild under way. Here both live
// in Valkey (tasks.Queue for the request and its dedupe, a back-off record
// per person), shared by every replica and worker.
package airows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/valkey-io/valkey-go"

	"movienight/server/internal/clients"
	"movienight/server/internal/config"
	"movienight/server/internal/jsonx"
	"movienight/server/internal/store"
	"movienight/server/internal/tasks"
	"movienight/server/internal/titles"
)

// The rows' shape (Home shows five at a time, three sets).
const (
	Sets             = 3
	RowsPerSet       = 5
	MaxRows          = Sets * RowsPerSet
	MaxItemsPerRow   = 12
	CacheVersion     = 4
	QueueName        = "ai-rows"
	matchConcurrency = 8
)

// Back-off after a failed rebuild (docs/limits.md, Recommendations): the
// next try waits RetryFirst, twice as long after each failure in a row, at
// most RetryMax; a good rebuild resets it.
const (
	RetryFirst = 10 * time.Minute
	RetryMax   = 6 * time.Hour
	// buildHold: the longest a rebuild may take before another may be asked
	// for (the model's timeout and the TMDB matching, with room).
	buildHold = 15 * time.Minute
)

// SystemPrompt is the model's brief (the Python's, word for word: the rows
// people see were tuned on it).
var SystemPrompt = "You curate themed movie rows for a personal movie app's home screen, like " +
	"a streaming service. Given the user's watch history and ratings, invent " +
	fmt.Sprintf("exactly %d themed rows. Spread the rows across the whole of their taste: ", MaxRows) +
	"each row should draw on a different part of the history and the highest " +
	"ratings, not only on the most recent film, and no two rows should feel alike. " +
	"Each row has a short, specific theme title (max 6 words, no clichés like " +
	fmt.Sprintf("'Hidden Gems') and %d real, existing feature films that fit the theme and ", MaxItemsPerRow) +
	"that the user has NOT already watched; a film appears in one row only. Give " +
	"each film as 'Title (Year)'. Mix well-known and lesser-known picks. Respond " +
	`with strict JSON only: {"rows": [{"theme": "...", "titles": ["Title (Year)", ...]}]}`

// Row is one cached row: its theme and the titles' keys.
type Row struct {
	Theme string   `json:"theme"`
	Keys  []string `json:"keys"`
}

type payload struct {
	V     int            `json:"v"`
	Rows  []Row          `json:"rows"`
	Basis map[string]any `json:"basis"`
}

// Service builds and serves the rows.
type Service struct {
	cfg     *config.Config
	pool    *pgxpool.Pool
	q       *store.Queries
	kv      valkey.Client
	clients *clients.Set
	titles  *titles.Service
	queue   *tasks.Queue
	log     *slog.Logger
	now     func() time.Time
}

// New makes the service. The API (Home: Serve, Note) and the worker
// (RunQueued) each make one on the same Valkey, which is the whole contract
// between them: the queue "ai-rows" and the back-off records.
func New(cfg *config.Config, pool *pgxpool.Pool, kv valkey.Client, cl *clients.Set, t *titles.Service, log *slog.Logger) *Service {
	return &Service{cfg: cfg, pool: pool, q: store.New(pool), kv: kv, clients: cl, titles: t,
		queue: tasks.New(kv, QueueName), log: log, now: time.Now}
}

// ---------------------------------------------------------------------------
// API side
// ---------------------------------------------------------------------------

// failure is a person's back-off record.
type failure struct {
	Count int       `json:"count"`
	Why   string    `json:"why"`
	Until time.Time `json:"until"`
}

func (s *Service) failKey(userID int32) string { return "ai-rows:fail:" + strconv.Itoa(int(userID)) }

func (s *Service) failureOf(ctx context.Context, userID int32) (*failure, error) {
	raw, err := s.kv.Do(ctx, s.kv.B().Get().Key(s.failKey(userID)).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f failure
	if json.Unmarshal([]byte(raw), &f) != nil {
		return nil, nil
	}
	return &f, nil
}

// Serve is the person's cached rows and what the model read for them
// (basis), and asks for a rebuild when they are stale: older than
// AI_ROWS_CACHE_TTL_HOURS, AI_REFRESH_AFTER_WATCHES new watches since, made
// by another model, or none at all. Never waits for the model.
func (s *Service) Serve(ctx context.Context, userID int32) ([]Row, map[string]any, error) {
	if !s.cfg.AIEnabled() {
		return nil, nil, nil
	}
	latest, err := s.q.LatestAIRows(ctx, userID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, err
	}
	var p payload
	have := err == nil && json.Unmarshal([]byte(latest.PayloadJson), &p) == nil && p.V == CacheVersion && len(p.Rows) > 0
	stale := true
	if have && latest.Model != nil && *latest.Model == s.cfg.LiteLLMModel {
		fresh := !latest.GeneratedAt.Before(s.now().Add(-time.Duration(s.cfg.AIRowsCacheTTLHours) * time.Hour))
		n, err := s.q.AIRowsWatchesSince(ctx, store.AIRowsWatchesSinceParams{UserID: userID, Since: latest.GeneratedAt,
			Cap: int32(s.cfg.AIRefreshAfterWatches)})
		if err != nil {
			return nil, nil, err
		}
		stale = !fresh || int(n) >= s.cfg.AIRefreshAfterWatches
	}
	if stale {
		if err := s.Request(ctx, userID); err != nil {
			// The cached rows still serve; the request is retried on the
			// next visit. Logged, not hidden.
			s.log.ErrorContext(ctx, "AI rows rebuild could not be asked for", "user", userID, "err", err)
		}
	}
	if !have {
		return []Row{}, nil, nil
	}
	return p.Rows, p.Basis, nil
}

// Request asks a worker to rebuild the person's rows, unless one is
// already asked for or under way, or a failed one's back-off lasts.
func (s *Service) Request(ctx context.Context, userID int32) error {
	f, err := s.failureOf(ctx, userID)
	if err != nil {
		return err
	}
	if f != nil && s.now().Before(f.Until) {
		return nil
	}
	_, err = s.queue.Enqueue(ctx, strconv.Itoa(int(userID)), 0, buildHold)
	return err
}

// Note is what Home says where "Picked for you" would be when it has no
// rows: why it could not be made, or that it is being made; nil otherwise.
func (s *Service) Note(ctx context.Context, userID int32, haveRows bool) (*string, error) {
	if haveRows || !s.cfg.AIEnabled() {
		return nil, nil
	}
	f, err := s.failureOf(ctx, userID)
	if err != nil {
		return nil, err
	}
	if f != nil {
		n := fmt.Sprintf("Picked for you couldn't be made just now: %s. Movie Night tries again by itself %s.",
			f.Why, inAWhile(f.Until.Sub(s.now())))
		return &n, nil
	}
	pending, err := s.queue.Pending(ctx, strconv.Itoa(int(userID)))
	if err != nil || !pending {
		return nil, err
	}
	n := "Picked for you is being made from your history; it shows here in a minute or two."
	return &n, nil
}

func inAWhile(d time.Duration) string {
	minutes := max(1, int(math.Round(d.Minutes())))
	if minutes < 90 {
		unit := "minutes"
		if minutes == 1 {
			unit = "minute"
		}
		return fmt.Sprintf("in about %d %s", minutes, unit)
	}
	return fmt.Sprintf("in about %d hours", int(math.Round(float64(minutes)/60)))
}

// ---------------------------------------------------------------------------
// Worker side
// ---------------------------------------------------------------------------

// RunQueued serves rebuild requests until ctx ends (every worker runs it;
// each request goes to one).
func (s *Service) RunQueued(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		due, err := s.queue.Due(ctx, 4)
		if err != nil {
			if ctx.Err() == nil {
				s.log.ErrorContext(ctx, "AI rows queue unreadable", "err", err)
			}
			continue
		}
		for _, m := range due {
			s.runOne(ctx, m)
		}
	}
}

func (s *Service) runOne(ctx context.Context, member string) {
	defer func() {
		// Released after the build (on a fresh context, so a shutdown that
		// cancelled it still frees the person for the next request).
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = s.queue.Release(c, member)
	}()
	n, err := strconv.Atoi(member)
	if err != nil {
		s.log.ErrorContext(ctx, "AI rows queue: not a person id; dropped", "member", member)
		return
	}
	id := int32(n)
	rows, err := s.Generate(ctx, id)
	switch {
	case ctx.Err() != nil:
	case err != nil:
		s.log.ErrorContext(ctx, "AI rows failed", "user", id, "err", err)
		s.failed(ctx, id, "the AI didn't answer")
	case len(rows) == 0:
		s.failed(ctx, id, "none of the AI's picks could be found on TMDB")
	default:
		_ = s.kv.Do(ctx, s.kv.B().Del().Key(s.failKey(id)).Build()).Error()
	}
}

// failed records a failure and its back-off.
func (s *Service) failed(ctx context.Context, userID int32, why string) {
	prev, _ := s.failureOf(ctx, userID)
	count := 1
	if prev != nil {
		count = prev.Count + 1
	}
	wait := RetryMax
	if count <= 10 {
		wait = min(RetryFirst*time.Duration(1<<(count-1)), RetryMax)
	}
	f := failure{Count: count, Why: why, Until: s.now().Add(wait)}
	raw, _ := json.Marshal(f)
	// Kept a while past its back-off, so failures in a row keep doubling.
	if err := s.kv.Do(ctx, s.kv.B().Set().Key(s.failKey(userID)).Value(string(raw)).Px(wait+RetryMax).Build()).Error(); err != nil {
		s.log.ErrorContext(ctx, "AI rows back-off could not be recorded", "user", userID, "err", err)
	}
	s.log.WarnContext(ctx, "AI rows failed; waiting before the next try", "user", userID, "why", why, "minutes", int(wait.Minutes()))
}

// Generate asks the model for MaxRows themed rows from the person's history
// and ratings, matches each pick on TMDB and stores the result, replacing
// the rows before. A pick TMDB cannot match is skipped; when no pick at all
// is found nothing is stored (the rows before stay) and the answer is
// empty. Every TMDB search failing is an error, not "nothing found".
func (s *Service) Generate(ctx context.Context, userID int32) ([]Row, error) {
	summary, basis, err := s.historySummary(ctx, userID)
	if err != nil {
		return nil, err
	}
	raw, err := s.clients.AI.ChatJSON(ctx, SystemPrompt,
		fmt.Sprintf("User's history:\n%s\n\nGenerate the %d rows now.", summary, MaxRows), 2)
	if err != nil {
		return nil, err
	}
	picks, err := ParseResponse(raw)
	if err != nil {
		return nil, err
	}
	found, failures, tries := s.match(ctx, picks)
	if tries > 0 && failures == tries {
		return nil, fmt.Errorf("TMDB didn't answer any of the %d searches", tries)
	}
	var out []Row
	used := map[string]bool{}
	for i, row := range picks {
		// The picks' details warm the title cache (written only where that
		// changes something), so Home draws the cards without TMDB.
		metas := titles.NormalizeAll(found[i], "movie")
		s.titles.Remember(ctx, metas)
		var keys []string
		for _, m := range metas {
			if !used[m.Key] {
				used[m.Key] = true
				keys = append(keys, m.Key)
			}
		}
		if len(keys) > 0 {
			out = append(out, Row{Theme: row.Theme, Keys: keys})
		}
	}
	if len(out) == 0 {
		s.log.InfoContext(ctx, "AI rows: none of the picks were found on TMDB; nothing stored", "user", userID)
		return nil, nil
	}
	body, err := json.Marshal(payload{V: CacheVersion, Rows: out, Basis: basis})
	if err != nil {
		return nil, err
	}
	model := s.cfg.LiteLLMModel
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		id, err := q.AIRowsInsert(ctx, store.AIRowsInsertParams{UserID: userID, Model: &model, PayloadJson: string(body)})
		if err != nil {
			return err
		}
		return q.AIRowsDeleteOthers(ctx, store.AIRowsDeleteOthersParams{UserID: userID, ID: id})
	})
	if err != nil {
		return nil, err
	}
	s.log.InfoContext(ctx, "AI rows made", "user", userID, "rows", len(out))
	return out, nil
}

// match finds every pick on TMDB, matchConcurrency at a time (network
// only). found[i] is row i's results in order (nil where none matched).
func (s *Service) match(ctx context.Context, picks []Pick) (found [][]jsonx.Obj, failures, tries int) {
	found = make([][]jsonx.Obj, len(picks))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, matchConcurrency)
	for i, row := range picks {
		found[i] = make([]jsonx.Obj, len(row.Titles))
		for j, text := range row.Titles {
			wg.Add(1)
			sem <- struct{}{}
			go func(i, j int, text string) {
				defer wg.Done()
				defer func() { <-sem }()
				hit, err := s.resolve(ctx, text)
				mu.Lock()
				defer mu.Unlock()
				tries++
				if err != nil {
					failures++
					return
				}
				found[i][j] = hit
			}(i, j, text)
		}
	}
	wg.Wait()
	for i := range found {
		kept := found[i][:0]
		for _, r := range found[i] {
			if r != nil {
				kept = append(kept, r)
			}
		}
		found[i] = kept
	}
	return found, failures, tries
}

var titleYearRe = regexp.MustCompile(`^(.*?)\s*\((\d{4})\)\s*$`)

// SplitTitleYear reads "Title (Year)".
func SplitTitleYear(text string) (string, int) {
	if m := titleYearRe.FindStringSubmatch(text); m != nil {
		y, _ := strconv.Atoi(m[2])
		return strings.TrimSpace(m[1]), y
	}
	return strings.TrimSpace(text), 0
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func norm(s string) string { return nonAlnum.ReplaceAllString(strings.ToLower(s), "") }

func yearOf(r jsonx.Obj) int {
	d := jsonx.Str(r, "release_date")
	if len(d) >= 4 {
		if y, err := strconv.Atoi(d[:4]); err == nil {
			return y
		}
	}
	return 0
}

// resolve matches "Title (Year)" to a TMDB film: exact title and year, then
// the title with a year one off, then the exact title, then TMDB's top hit.
// nil, nil: nothing found.
func (s *Service) resolve(ctx context.Context, text string) (jsonx.Obj, error) {
	name, year := SplitTitleYear(text)
	if name == "" {
		return nil, nil
	}
	movies, err := s.clients.TMDB.SearchMovie(ctx, name, year)
	if err == nil && len(movies) == 0 && year != 0 {
		movies, err = s.clients.TMDB.SearchMovie(ctx, name, 0)
	}
	if err != nil || len(movies) == 0 {
		return nil, err
	}
	target := norm(name)
	for _, r := range movies {
		if norm(jsonx.Str(r, "title")) == target && (year == 0 || yearOf(r) == year) {
			return r, nil
		}
	}
	if year != 0 {
		for _, r := range movies {
			ry, t := yearOf(r), norm(jsonx.Str(r, "title"))
			if ry != 0 && abs(ry-year) <= 1 && (strings.Contains(t, target) || strings.Contains(target, t)) {
				return r, nil
			}
		}
	}
	for _, r := range movies {
		if norm(jsonx.Str(r, "title")) == target || norm(jsonx.Str(r, "original_title")) == target {
			return r, nil
		}
	}
	return movies[0], nil
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
