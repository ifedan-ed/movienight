package libraries

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"movienight/server/internal/clients"
	"movienight/server/internal/media/naming"
	"movienight/server/internal/media/scan"
	"movienight/server/internal/store"
	"movienight/server/internal/titles"
)

// AskScan asks a worker to scan the library soon. Asking again while one is
// waiting or running adds nothing.
func (s *Service) AskScan(ctx context.Context, id int32) error {
	if !s.Enabled() {
		return errOff
	}
	if _, err := s.get(ctx, id); err != nil {
		return err
	}
	added, err := s.queue.Enqueue(ctx, strconv.Itoa(int(id)), 0, scanHold)
	if err != nil {
		return err
	}
	if added {
		s.setProgress(ctx, id, Progress{State: "queued"})
	}
	return nil
}

// RunQueued serves the scan queue until ctx ends (every worker runs it;
// each scan goes to exactly one).
func (s *Service) RunQueued(ctx context.Context) {
	t := time.NewTicker(pollEvery)
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
				s.log.ErrorContext(ctx, "scan queue unreadable", "err", err)
			}
			continue
		}
		for _, member := range due {
			n, err := strconv.Atoi(member)
			if err != nil {
				continue
			}
			id := int32(n)
			res, err := s.ScanLocked(ctx, id)
			// Released after the scan: asking during it joins it.
			_ = s.queue.Release(context.WithoutCancel(ctx), member)
			s.logScan(ctx, id, res, err)
		}
	}
}

// ScanAll scans every library, one after another (the scheduled job).
func (s *Service) ScanAll(ctx context.Context) error {
	if !s.Enabled() {
		return nil
	}
	ids, err := s.q.MediaLibraryIDs(ctx)
	if err != nil {
		return err
	}
	var failed []string
	for _, id := range ids {
		res, err := s.ScanLocked(ctx, id)
		s.logScan(ctx, id, res, err)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failed = append(failed, strconv.Itoa(int(id)))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("libraries %s could not be scanned", strings.Join(failed, ", "))
	}
	return nil
}

func (s *Service) logScan(ctx context.Context, id int32, res Result, err error) {
	switch {
	case errors.Is(err, errBusy):
	case err != nil:
		s.log.WarnContext(ctx, "library scan failed", "library", id, "err", err)
	default:
		s.log.InfoContext(ctx, "library scan", "library", id, "result", res.String())
	}
}

var errBusy = errors.New("this library is being scanned already")

// ScanLocked is Scan under the library's lock: two workers never scan one
// library at once (the queue and the schedule can both ask).
func (s *Service) ScanLocked(ctx context.Context, id int32) (Result, error) {
	member := strconv.Itoa(int(id))
	ok, err := s.queue.Lock(ctx, member, scanHold)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		return Result{}, errBusy
	}
	defer func() { _ = s.queue.Unlock(context.WithoutCancel(ctx), member) }()
	defer s.clearProgress(ctx, id)
	return s.Scan(ctx, id)
}

// Result is what one scan did.
type Result struct {
	Seen, Probed, Added, Removed, Unreadable, Unmatched int
}

func (r Result) String() string {
	return fmt.Sprintf("%d files, %d probed, %d removed, %d unreadable, %d unmatched", r.Seen, r.Probed, r.Removed,
		r.Unreadable, r.Unmatched)
}

// Scan scans one library: the media service walks its folders and probes
// what changed; each file is named, matched and stored as it arrives, in
// batches. Files gone from a folder that was read are removed; a folder
// that could not be read keeps its files as they are. A scan cut short
// (the media service went away) removes nothing and is recorded as
// failed. Readable files still without a title are matched again.
func (s *Service) Scan(ctx context.Context, id int32) (Result, error) {
	var res Result
	if !s.Enabled() {
		return res, errOff
	}
	lib, err := s.get(ctx, id)
	if err != nil {
		return res, err
	}
	kind := naming.Kind(lib.Kind)
	media := mediaOf(lib.Kind)
	knownRows, err := s.q.MediaKnown(ctx, id)
	if err != nil {
		return res, err
	}
	known := make([]scan.Known, len(knownRows))
	for i, k := range knownRows {
		known[i] = scan.Known{Path: k.Path, Size: k.Size, MTime: k.Mtime, Subs: k.SubsSig}
	}
	hand := map[string]int{}
	hands, err := s.q.MediaHandMatches(ctx, id)
	if err != nil {
		return res, err
	}
	for _, h := range hands {
		hand[handKey(h.ParsedTitle, int(deref32(h.ParsedYear)))] = int(h.TmdbID)
	}
	m := newMatcher(s.tmdb(), hand)
	s.setProgress(ctx, id, Progress{State: "scanning"})

	var batch []store.MediaFileUpsertParams
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := execBatch(s.q.MediaFileUpsert(ctx, batch))
		batch = batch[:0]
		return err
	}
	folders := []FolderProblem{}
	var missing []string
	err = s.media.Scan(ctx, scan.Request{Roots: lib.Paths, Kind: kind, Known: known}, func(e scan.Event) error {
		switch e.Type {
		case scan.EventProgress:
			s.setProgress(ctx, id, Progress{State: "scanning", Seen: e.Seen, ToProbe: e.ToProbe, Probed: e.Probed})
		case scan.EventRoot:
			folders = append(folders, FolderProblem{Path: e.Root, Error: e.Error})
		case scan.EventFile:
			p, err := s.fileRow(ctx, m, lib, kind, media, e)
			if err != nil {
				return err
			}
			if p.Probe == nil {
				res.Unreadable++
			} else if p.TmdbID == nil {
				res.Unmatched++
			}
			res.Probed++
			batch = append(batch, p)
			if len(batch) >= batchSize {
				return flush()
			}
		case scan.EventDone:
			res.Seen = e.Seen
			missing = e.Missing
		}
		return nil
	})
	if ferr := flush(); err == nil {
		err = ferr
	}
	problems, _ := json.Marshal(folders)
	if err != nil {
		if ctx.Err() == nil {
			msg := err.Error()
			_ = s.q.MediaLibraryScanned(context.WithoutCancel(ctx), store.MediaLibraryScannedParams{ID: id, Error: &msg, Problems: problems})
		}
		return res, err
	}
	if len(missing) > 0 {
		n, err := s.q.MediaFilesDelete(ctx, store.MediaFilesDeleteParams{LibraryID: id, Paths: missing})
		if err != nil {
			return res, err
		}
		res.Removed = int(n)
	}
	if err := s.rematch(ctx, m, kind, id, &res); err != nil {
		return res, err
	}
	if err := s.q.MediaLibraryScanned(ctx, store.MediaLibraryScannedParams{ID: id, Problems: problems}); err != nil {
		return res, err
	}
	if res.Probed > 0 || res.Removed > 0 {
		if err := s.Reindex(ctx); err != nil {
			// The files are stored; search catches up on the next scan.
			s.log.WarnContext(ctx, "library search index not updated", "err", err)
		}
	}
	return res, nil
}

// fileRow names and matches one scanned file. Only TMDB failing is an
// error, and it does not stop the scan: the file is stored unmatched and
// matched again next time.
func (s *Service) fileRow(ctx context.Context, m *matcher, lib store.MediaLibrary, kind naming.Kind, media string,
	e scan.Event) (store.MediaFileUpsertParams, error) {
	parsed := naming.Parse(relTo(e.Path, lib.Paths), kind)
	p := store.MediaFileUpsertParams{LibraryID: lib.ID, Path: e.Path, Size: e.Size, Mtime: e.MTime, SubsSig: e.Subs,
		MediaType: media, ParsedTitle: parsed.Title, ParsedYear: nz(parsed.Year)}
	if kind == naming.Shows && parsed.Episode > 0 {
		p.Season, p.Episode, p.EpisodeEnd = ptr32(parsed.Season), ptr32(parsed.Episode), nz(parsed.EpisodeEnd)
	}
	if e.Probe == nil {
		why := "Can't be read"
		if e.Error != "" {
			why = "Can't be read: " + e.Error
		}
		p.Problem = &why
		return p, nil
	}
	raw, err := json.Marshal(e.Probe)
	if err != nil {
		return p, err
	}
	p.Probe = raw
	if kind == naming.Shows && parsed.Episode == 0 {
		why := "No season and episode in the name"
		p.Problem = &why
		return p, nil
	}
	mt, problem, err := m.match(ctx, parsed, kind)
	switch {
	case err != nil:
		why := "TMDB didn't answer; matched again on the next scan"
		p.Problem = &why
		s.log.WarnContext(ctx, "matching failed", "path", e.Path, "err", err)
	case problem != "":
		p.Problem = &problem
	default:
		p.TmdbID, p.MatchedBy = ptr32(mt.TMDBID), &mt.By
	}
	return p, nil
}

// rematch matches again the readable files still without a title.
func (s *Service) rematch(ctx context.Context, m *matcher, kind naming.Kind, id int32, res *Result) error {
	rows, err := s.q.MediaUnmatched(ctx, id)
	if err != nil || len(rows) == 0 {
		return err
	}
	var updates []store.MediaSetMatchParams
	for _, r := range rows {
		if kind == naming.Shows && r.Episode == nil {
			continue
		}
		p := naming.Parsed{Title: r.ParsedTitle, Year: int(deref32(r.ParsedYear))}
		// Ids in the name are read again from the path (they are not stored).
		again := naming.Parse(r.Path, kind)
		p.TMDB, p.IMDb, p.TVDB = again.TMDB, again.IMDb, again.TVDB
		mt, problem, err := m.match(ctx, p, kind)
		if err != nil {
			continue // TMDB still failing: next time
		}
		if problem != "" {
			continue // still nothing: the problem stands as it is
		}
		updates = append(updates, store.MediaSetMatchParams{ID: r.ID, TmdbID: ptr32(mt.TMDBID), MatchedBy: &mt.By})
		res.Unmatched--
	}
	res.Unmatched = max(res.Unmatched, 0)
	if len(updates) == 0 {
		return nil
	}
	return execBatch(s.q.MediaSetMatch(ctx, updates))
}

// ---------------------------------------------------------------------------
// The search index
// ---------------------------------------------------------------------------

// IndexPrefix starts this package's documents in the library index.
const IndexPrefix = clients.LibraryOwnPrefix

// Reindex makes the library search index's own documents match the titles
// the libraries hold: new ones added (with TMDB's details from the title
// cache), gone ones removed. Jellyfin's documents are left alone.
func (s *Service) Reindex(ctx context.Context) error {
	rows, err := s.q.MediaTitles(ctx)
	if err != nil {
		return err
	}
	keys := make([]string, len(rows))
	for i, r := range rows {
		keys[i] = titles.Key(r.MediaType, int(r.TmdbID))
	}
	metas, err := s.titles.Ensure(ctx, keys, len(keys))
	if err != nil {
		return err
	}
	m := s.meili()
	if err := m.EnsureIndex(ctx, clients.LibraryIndex); err != nil {
		return err
	}
	current, err := m.FieldValues(ctx, clients.LibraryIndex, "searchText")
	if err != nil {
		return err
	}
	want := map[string]bool{}
	var changed []clients.JSON
	for _, r := range rows {
		meta := metas[titles.Key(r.MediaType, int(r.TmdbID))]
		if meta == nil || meta.Title == "" {
			continue
		}
		d := doc(meta)
		id := d["id"].(string)
		want[id] = true
		if cur, ok := current[id]; !ok || cur != d["searchText"] {
			changed = append(changed, d)
		}
	}
	for i := 0; i < len(changed); i += 500 {
		if _, err := m.ReplaceDocuments(ctx, clients.LibraryIndex, changed[i:min(i+500, len(changed))]); err != nil {
			return err
		}
	}
	var gone []string
	for id := range current {
		if strings.HasPrefix(id, IndexPrefix) && !want[id] {
			gone = append(gone, id)
		}
	}
	return m.DeleteDocuments(ctx, clients.LibraryIndex, gone)
}

// doc is a title's library search document, in the shape Jellyfin's are
// (clients.LibraryDoc), from TMDB's details.
func doc(meta *titles.Meta) clients.JSON {
	original := meta.Title
	if meta.OriginalTitle != nil && *meta.OriginalTitle != "" {
		original = *meta.OriginalTitle
	}
	overview := ""
	if meta.Overview != nil {
		overview = *meta.Overview
	}
	parts := []string{meta.Title}
	if original != meta.Title {
		parts = append(parts, original)
	}
	var year any
	if meta.Year != nil && *meta.Year > 0 {
		year = *meta.Year
		parts = append(parts, fmt.Sprintf("(%d)", *meta.Year))
	}
	genres := meta.Genres
	if genres == nil {
		genres = []string{}
	}
	if len(genres) > 0 {
		parts = append(parts, strings.Join(genres, ", "))
	}
	if overview != "" {
		parts = append(parts, overview)
	}
	return clients.JSON{"id": IndexPrefix + meta.MediaType + "-" + strconv.Itoa(meta.TMDBID), "tmdb_id": meta.TMDBID,
		"media_type": meta.MediaType, "title": meta.Title, "original_title": original, "overview": overview,
		"genres": genres, "people": []string{}, "year": year, "searchText": strings.Join(parts, " | ")}
}

// execBatch runs a sqlc batch and returns its first error.
func execBatch(br interface {
	Exec(func(int, error))
	Close() error
}) error {
	var first error
	br.Exec(func(_ int, err error) {
		if err != nil && first == nil {
			first = err
		}
	})
	if err := br.Close(); err != nil && first == nil {
		first = err
	}
	return first
}

func nz(n int) *int32 {
	if n == 0 {
		return nil
	}
	return ptr32(n)
}

func deref32(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}
