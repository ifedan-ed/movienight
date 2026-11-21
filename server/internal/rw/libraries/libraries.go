// Package libraries is the routes of Movie Night's own media libraries:
// the Libraries page (list, add, change, remove, scan now, the files with
// problems and fixing a match). All are for admins. The work is
// internal/libraries'; a scan runs on a worker.
package libraries

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"movienight/server/internal/auth"
	"movienight/server/internal/libraries"
	"movienight/server/internal/rw"
	"movienight/server/internal/rw/schema"
)

type libraryPath struct {
	ID int32 `path:"library_id" minimum:"1"`
}

// Register adds the routes.
func Register(d *rw.Deps) {
	lib := d.Libraries
	admin := func(ctx context.Context) error {
		id, err := d.User(ctx)
		if err != nil {
			return err
		}
		return auth.RequireAdmin(id)
	}

	huma.Register(d.Huma(), rw.Op("libraries_api_v1_libraries_get", http.MethodGet, "/api/v1/libraries", "Libraries", "libraries",
		"Movie Night's own media libraries, each with its counts and any scan under way."),
		func(ctx context.Context, _ *struct{}) (*rw.Body[schema.LibrariesOut], error) {
			if err := admin(ctx); err != nil {
				return nil, err
			}
			out := schema.LibrariesOut{Enabled: lib.Enabled(), Libraries: []schema.Library{}, Folders: []string{}}
			if !lib.Enabled() {
				return rw.Out(out), nil
			}
			all, err := lib.List(ctx)
			if err != nil {
				return nil, d.Fail(ctx, err)
			}
			for _, l := range all {
				out.Libraries = append(out.Libraries, libraryOut(l))
			}
			// The media service's folders, for the add form. Its trouble is
			// shown, not an error: the libraries still list.
			sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if st, err := lib.Media().Status(sctx); err != nil {
				msg := err.Error()
				out.MediaError = &msg
			} else if st.Folders != nil {
				out.Folders = st.Folders
			}
			return rw.Out(out), nil
		})

	huma.Register(d.Huma(), huma.Operation{OperationID: "library_add_api_v1_libraries_post", Method: http.MethodPost,
		Path: "/api/v1/libraries", Summary: "Add Library", Tags: []string{"libraries"}, DefaultStatus: http.StatusCreated,
		Description: "Adds a library and asks for its first scan."},
		func(ctx context.Context, in *rw.Body[schema.LibraryIn]) (*rw.Body[schema.LibraryCreated], error) {
			if err := admin(ctx); err != nil {
				return nil, err
			}
			id, err := lib.Create(ctx, libraries.Settings{Name: in.Body.Name, Kind: in.Body.Kind, Paths: in.Body.Paths})
			if err != nil {
				return nil, d.Fail(ctx, err)
			}
			return rw.Out(schema.LibraryCreated{ID: id}), nil
		})

	huma.Register(d.Huma(), rw.Op("library_update_api_v1_libraries__library_id__put", http.MethodPut, "/api/v1/libraries/{library_id}",
		"Change Library", "libraries", "Renames a library or changes its folders, and asks for a scan."),
		func(ctx context.Context, in *struct {
			libraryPath
			Body schema.LibraryIn
		}) (*rw.Body[schema.Ok], error) {
			if err := admin(ctx); err != nil {
				return nil, err
			}
			if err := lib.Update(ctx, in.ID, libraries.Settings{Name: in.Body.Name, Kind: in.Body.Kind, Paths: in.Body.Paths}); err != nil {
				return nil, d.Fail(ctx, err)
			}
			return rw.Out(schema.Ok{Ok: true}), nil
		})

	huma.Register(d.Huma(), rw.Op("library_delete_api_v1_libraries__library_id__delete", http.MethodDelete, "/api/v1/libraries/{library_id}",
		"Remove Library", "libraries", "Removes a library from Movie Night. The files stay where they are."),
		func(ctx context.Context, in *libraryPath) (*rw.Body[schema.Ok], error) {
			if err := admin(ctx); err != nil {
				return nil, err
			}
			if err := lib.Delete(ctx, in.ID); err != nil {
				return nil, d.Fail(ctx, err)
			}
			return rw.Out(schema.Ok{Ok: true}), nil
		})

	huma.Register(d.Huma(), huma.Operation{OperationID: "library_scan_api_v1_libraries__library_id__scan_post", Method: http.MethodPost,
		Path: "/api/v1/libraries/{library_id}/scan", Summary: "Scan Library", Tags: []string{"libraries"}, DefaultStatus: http.StatusAccepted,
		Description: "Scan now: a worker scans the library (only new and changed files are read)."},
		func(ctx context.Context, in *libraryPath) (*rw.Body[schema.Ok], error) {
			if err := admin(ctx); err != nil {
				return nil, err
			}
			if err := lib.AskScan(ctx, in.ID); err != nil {
				return nil, d.Fail(ctx, err)
			}
			return rw.Out(schema.Ok{Ok: true}), nil
		})

	huma.Register(d.Huma(), rw.Op("library_problems_api_v1_libraries__library_id__problems_get", http.MethodGet,
		"/api/v1/libraries/{library_id}/problems", "Library Problems", "libraries",
		"Files that can't be read, or whose name matched no film or show."),
		func(ctx context.Context, in *libraryPath) (*rw.Body[schema.LibraryProblemsOut], error) {
			if err := admin(ctx); err != nil {
				return nil, err
			}
			got, err := lib.Problems(ctx, in.ID)
			if err != nil {
				return nil, d.Fail(ctx, err)
			}
			out := schema.LibraryProblemsOut{Items: make([]schema.LibraryProblem, len(got))}
			for i, p := range got {
				out.Items[i] = schema.LibraryProblem{FileID: p.FileID, Path: p.Path, Problem: p.Problem, Title: p.Title,
					Year: p.Year, Season: p.Season, Episode: p.Episode, Readable: p.Readable}
			}
			return rw.Out(out), nil
		})

	huma.Register(d.Huma(), rw.Op("library_match_api_v1_libraries_files__file_id__match_put", http.MethodPut,
		"/api/v1/libraries/files/{file_id}/match", "Match File", "libraries",
		"Matches a file to a TMDB title by hand, with every file named like it (a show's episodes). It stays over rescans."),
		func(ctx context.Context, in *struct {
			FileID int64 `path:"file_id" minimum:"1"`
			Body   schema.LibraryMatchIn
		}) (*rw.Body[schema.LibraryMatchOut], error) {
			if err := admin(ctx); err != nil {
				return nil, err
			}
			n, err := lib.FixMatch(ctx, in.FileID, in.Body.TMDBID)
			if err != nil {
				return nil, d.Fail(ctx, err)
			}
			return rw.Out(schema.LibraryMatchOut{Files: n}), nil
		})
}

func libraryOut(l libraries.Library) schema.Library {
	out := schema.Library{ID: l.ID, Name: l.Name, Kind: l.Kind, Paths: l.Paths, Files: l.Files, Titles: l.Titles,
		Problems: l.Problems, ScanError: l.ScanError, Folders: []schema.FolderProblem{}}
	if l.ScannedAt != nil {
		at := rw.ISOTime(*l.ScannedAt)
		out.ScannedAt = &at
	}
	for _, f := range l.FolderProblems {
		out.Folders = append(out.Folders, schema.FolderProblem{Path: f.Path, Error: f.Error})
	}
	if p := l.Scan; p != nil {
		out.Scan = &schema.LibraryScan{State: p.State, Seen: p.Seen, ToProbe: p.ToProbe, Probed: p.Probed}
	}
	return out
}
