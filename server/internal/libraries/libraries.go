// Package libraries is the API's and the worker's side of Movie Night's
// own media libraries: the libraries an admin sets up, scanning them
// through the media service, matching what it finds to TMDB, and the
// lookups playback needs (a title's file, a show's episodes).
//
// What a scan finds is stored per file in media_files. A file is in the
// library once it is readable and matched; the view library_titles puts
// those next to Jellyfin's, so Home, cards, search and the digests treat
// both sources alike.
//
// The API only asks for scans (a Valkey queue); a worker runs them, and
// the scheduled job rescans every library every 30 minutes. A scan is
// incremental: the media service probes only new and changed files.
package libraries

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/valkey-io/valkey-go"

	"movienight/server/internal/apierr"
	"movienight/server/internal/clients"
	"movienight/server/internal/media/mediaclient"
	"movienight/server/internal/media/probe"
	"movienight/server/internal/store"
	"movienight/server/internal/tasks"
	"movienight/server/internal/titles"
)

// Limits of a library's settings (docs/limits.md).
const (
	MaxNameChars = 100
	MaxPaths     = 20
	MaxPathChars = 1000
	// ProblemsShown is how many problem files the Libraries page lists per
	// library; the count above them is the full number.
	ProblemsShown = 500
)

// The scan queue: the API asks, a worker scans.
const (
	QueueName = "media-scan"
	// scanHold is the longest one scan may take before the library can be
	// asked for again (a first scan of a very big library on slow disks).
	scanHold  = 6 * time.Hour
	pollEvery = time.Second
	// progressFor keeps a scan's progress readable a while after its last
	// update (a worker that died mid-scan leaves it to run out).
	progressFor = 10 * time.Minute
	batchSize   = 200
)

// Service runs the libraries.
type Service struct {
	pool   *pgxpool.Pool
	q      *store.Queries
	kv     valkey.Client
	media  *mediaclient.Client // nil: libraries are off (no MEDIA_URL)
	tmdb   func() Finder
	meili  func() *clients.Meili
	titles *titles.Service
	queue  *tasks.Queue
	log    *slog.Logger
}

// New makes the service. media is nil when MEDIA_URL is not set; tmdb and
// meili are read per call (tests swap the clients).
func New(pool *pgxpool.Pool, kv valkey.Client, media *mediaclient.Client, tmdb func() Finder,
	meili func() *clients.Meili, t *titles.Service, log *slog.Logger) *Service {
	return &Service{pool: pool, q: store.New(pool), kv: kv, media: media, tmdb: tmdb, meili: meili, titles: t,
		queue: tasks.New(kv, QueueName), log: log}
}

// Enabled reports whether this install has libraries (MEDIA_URL).
func (s *Service) Enabled() bool { return s.media != nil }

// Media is the media service's client (nil when off).
func (s *Service) Media() *mediaclient.Client { return s.media }

var errOff = apierr.New(http.StatusNotFound, "Media libraries are not set up on this install (MEDIA_URL)")

// ---------------------------------------------------------------------------
// The libraries
// ---------------------------------------------------------------------------

// Library is one library with its counts and scan.
type Library struct {
	ID        int32
	Name      string
	Kind      string // movies or shows
	Paths     []string
	Files     int
	Titles    int
	Problems  int
	ScannedAt *time.Time
	ScanError *string
	// Folders that could not be read on the last scan.
	FolderProblems []FolderProblem
	// Scan is the scan under way or asked for; nil when none.
	Scan *Progress
}

// FolderProblem is a folder a scan could not read.
type FolderProblem struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// Progress is a scan's progress, kept in Valkey while it runs.
type Progress struct {
	State   string `json:"state"` // queued or scanning
	Seen    int    `json:"seen"`
	ToProbe int    `json:"to_probe"`
	Probed  int    `json:"probed"`
}

func progressKey(id int32) string { return "media:scan:" + strconv.Itoa(int(id)) }

// List is every library.
func (s *Service) List(ctx context.Context) ([]Library, error) {
	rows, err := s.q.MediaLibraries(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Library, 0, len(rows))
	for _, r := range rows {
		l := Library{ID: r.ID, Name: r.Name, Kind: r.Kind, Paths: r.Paths, Files: int(r.Files), Titles: int(r.Titles),
			Problems: int(r.Problems), ScannedAt: r.ScannedAt, ScanError: r.ScanError, FolderProblems: []FolderProblem{}}
		_ = json.Unmarshal(r.ScanProblems, &l.FolderProblems)
		if l.Scan, err = s.progress(ctx, r.ID); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

func (s *Service) progress(ctx context.Context, id int32) (*Progress, error) {
	raw, err := s.kv.Do(ctx, s.kv.B().Get().Key(progressKey(id)).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p Progress
	if json.Unmarshal([]byte(raw), &p) != nil {
		return nil, nil
	}
	return &p, nil
}

func (s *Service) setProgress(ctx context.Context, id int32, p Progress) {
	b, _ := json.Marshal(p)
	if err := s.kv.Do(ctx, s.kv.B().Set().Key(progressKey(id)).Value(string(b)).Px(progressFor).Build()).Error(); err != nil {
		s.log.WarnContext(ctx, "scan progress not saved", "library", id, "err", err)
	}
}

func (s *Service) clearProgress(ctx context.Context, id int32) {
	_ = s.kv.Do(context.WithoutCancel(ctx), s.kv.B().Del().Key(progressKey(id)).Build()).Error()
}

// Settings are what an admin sets for a library.
type Settings struct {
	Name  string
	Kind  string
	Paths []string
}

// check cleans and checks a library's settings.
func (st *Settings) check() error {
	st.Name = strings.TrimSpace(st.Name)
	if st.Name == "" || utf8.RuneCountInString(st.Name) > MaxNameChars {
		return apierr.New(http.StatusUnprocessableEntity, fmt.Sprintf("A name of 1 to %d characters", MaxNameChars))
	}
	if st.Kind != "movies" && st.Kind != "shows" {
		return apierr.New(http.StatusUnprocessableEntity, "Films or shows")
	}
	var paths []string
	for _, p := range st.Paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.HasPrefix(p, "/") || len(p) > MaxPathChars || strings.ContainsRune(p, 0) {
			return apierr.New(http.StatusUnprocessableEntity, fmt.Sprintf("%q is not a full folder path (it starts with /)", p))
		}
		p = path.Clean(p)
		if !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 || len(paths) > MaxPaths {
		return apierr.New(http.StatusUnprocessableEntity, fmt.Sprintf("1 to %d folders", MaxPaths))
	}
	st.Paths = paths
	return nil
}

// Create adds a library and asks for its first scan.
func (s *Service) Create(ctx context.Context, st Settings) (int32, error) {
	if !s.Enabled() {
		return 0, errOff
	}
	if err := st.check(); err != nil {
		return 0, err
	}
	l, err := s.q.MediaLibraryCreate(ctx, store.MediaLibraryCreateParams{Name: st.Name, Kind: st.Kind, Paths: st.Paths})
	if err != nil {
		return 0, err
	}
	return l.ID, s.AskScan(ctx, l.ID)
}

// Update renames a library or changes its folders, and asks for a scan.
// The kind stays: its files were read as films or as episodes.
func (s *Service) Update(ctx context.Context, id int32, st Settings) error {
	if !s.Enabled() {
		return errOff
	}
	old, err := s.get(ctx, id)
	if err != nil {
		return err
	}
	if st.Kind == "" {
		st.Kind = old.Kind
	}
	if err := st.check(); err != nil {
		return err
	}
	if st.Kind != old.Kind {
		return apierr.New(http.StatusUnprocessableEntity, "A library's kind can't change: add another library")
	}
	if _, err := s.q.MediaLibraryUpdate(ctx, store.MediaLibraryUpdateParams{ID: id, Name: st.Name, Paths: st.Paths}); err != nil {
		return err
	}
	return s.AskScan(ctx, id)
}

// Delete removes a library and its files' records (never the files).
func (s *Service) Delete(ctx context.Context, id int32) error {
	if !s.Enabled() {
		return errOff
	}
	n, err := s.q.MediaLibraryDelete(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return apierr.NotFound()
	}
	s.clearProgress(ctx, id)
	if err := s.Reindex(ctx); err != nil {
		// The library is gone; its titles leave search on the next scan.
		s.log.WarnContext(ctx, "search index not updated after removing a library", "err", err)
	}
	return nil
}

func (s *Service) get(ctx context.Context, id int32) (store.MediaLibrary, error) {
	l, err := s.q.MediaLibrary(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, apierr.NotFound()
	}
	return l, err
}

// Problem is one file with a problem.
type Problem struct {
	FileID   int64
	Path     string // relative to its library folder
	Problem  string
	Title    string
	Year     *int32
	Season   *int32
	Episode  *int32
	TMDBID   *int32
	Readable bool
}

// Problems lists a library's files with a problem: unreadable, or matched
// to nothing.
func (s *Service) Problems(ctx context.Context, id int32) ([]Problem, error) {
	l, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.MediaProblems(ctx, store.MediaProblemsParams{LibraryID: id, Lim: ProblemsShown})
	if err != nil {
		return nil, err
	}
	out := make([]Problem, len(rows))
	for i, r := range rows {
		out[i] = Problem{FileID: r.ID, Path: relTo(r.Path, l.Paths), Problem: r.Problem, Title: r.ParsedTitle,
			Year: r.ParsedYear, Season: r.Season, Episode: r.Episode, TMDBID: r.TmdbID, Readable: r.Readable}
	}
	return out, nil
}

// FixMatch matches a file, and every file named like it in its library (a
// show's other episodes), to a TMDB title by hand. The match stays over
// rescans. It answers how many files changed.
func (s *Service) FixMatch(ctx context.Context, fileID int64, tmdbID int) (int, error) {
	if !s.Enabled() {
		return 0, errOff
	}
	f, err := s.q.MediaFile(ctx, fileID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, apierr.NotFound()
	}
	if err != nil {
		return 0, err
	}
	meta, err := s.titles.One(ctx, f.MediaType, tmdbID)
	if err != nil {
		return 0, apierr.New(http.StatusBadGateway, "TMDB didn't answer — try again")
	}
	if meta == nil || meta.Title == "" {
		kind := "film"
		if f.MediaType == "tv" {
			kind = "show"
		}
		return 0, apierr.New(http.StatusUnprocessableEntity, fmt.Sprintf("TMDB has no %s %d", kind, tmdbID))
	}
	id := int32(tmdbID)
	n, err := s.q.MediaMatchByHand(ctx, store.MediaMatchByHandParams{TmdbID: &id, LibraryID: f.LibraryID,
		MediaType: f.MediaType, ParsedTitle: f.ParsedTitle, ParsedYear: f.ParsedYear})
	if err != nil {
		return 0, err
	}
	if err := s.Reindex(ctx); err != nil {
		s.log.WarnContext(ctx, "search index not updated after a match by hand", "err", err)
	}
	return int(n), nil
}

// relTo is p relative to whichever of roots holds it.
func relTo(p string, roots []string) string {
	best := ""
	for _, r := range roots {
		r = strings.TrimRight(r, "/")
		if strings.HasPrefix(p, r+"/") && len(r) > len(best) {
			best = r
		}
	}
	if best == "" {
		return p
	}
	return p[len(best)+1:]
}

// ---------------------------------------------------------------------------
// Playback lookups
// ---------------------------------------------------------------------------

// File is a library file to play.
type File struct {
	ID      int64
	Path    string
	Probe   *probe.Probe
	Season  *int
	Episode *int
}

// FileFor is the file of a film (season and episode nil) or of one
// episode of a show; nil when the libraries have none.
func (s *Service) FileFor(ctx context.Context, media string, tmdbID int, season, episode *int) (*File, error) {
	if !s.Enabled() {
		return nil, nil
	}
	id := int32(tmdbID)
	r, err := s.q.MediaFileFor(ctx, store.MediaFileForParams{MediaType: media, TmdbID: &id, Season: i32(season), Episode: i32(episode)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p probe.Probe
	if err := json.Unmarshal(r.Probe, &p); err != nil {
		return nil, fmt.Errorf("a stored probe is not readable: %w", err)
	}
	f := &File{ID: r.ID, Path: r.Path, Probe: &p}
	if r.Season != nil {
		v := int(*r.Season)
		f.Season = &v
	}
	if episode != nil {
		v := *episode
		f.Episode = &v
	} else if r.Episode != nil {
		v := int(*r.Episode)
		f.Episode = &v
	}
	return f, nil
}

// Episodes are the (season, episode) pairs of a show the libraries hold,
// in order.
func (s *Service) Episodes(ctx context.Context, showID int) ([][2]int, error) {
	if !s.Enabled() {
		return nil, nil
	}
	rows, err := s.q.MediaEpisodes(ctx, ptr32(showID))
	if err != nil {
		return nil, err
	}
	out := make([][2]int, len(rows))
	for i, r := range rows {
		out[i] = [2]int{int(r.Season), int(r.Episode)}
	}
	return out, nil
}

func i32(n *int) *int32 {
	if n == nil {
		return nil
	}
	v := int32(*n)
	return &v
}

func ptr32(n int) *int32 { v := int32(n); return &v }

// mediaOf is the title kind of a library kind.
func mediaOf(kind string) string {
	if kind == "shows" {
		return "tv"
	}
	return "movie"
}
