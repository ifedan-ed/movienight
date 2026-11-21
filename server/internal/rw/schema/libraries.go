package schema

// LibraryScan is a scan under way or asked for.
type LibraryScan struct {
	State   string `json:"state" enum:"queued,scanning"`
	Seen    int    `json:"seen" doc:"videos found so far"`
	ToProbe int    `json:"to_probe" doc:"new or changed among them"`
	Probed  int    `json:"probed"`
}

// FolderProblem is a folder the last scan could not read.
type FolderProblem struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// Library is one of Movie Night's own media libraries.
type Library struct {
	ID        int32           `json:"id"`
	Name      string          `json:"name"`
	Kind      string          `json:"kind" enum:"movies,shows"`
	Paths     []string        `json:"paths" nullable:"false"`
	Files     int             `json:"files"`
	Titles    int             `json:"titles" doc:"films or shows found and matched"`
	Problems  int             `json:"problems" doc:"files that can't be read or matched"`
	ScannedAt *string         `json:"scanned_at" required:"false" nullable:"true"`
	ScanError *string         `json:"scan_error" required:"false" nullable:"true"`
	Folders   []FolderProblem `json:"folder_problems" nullable:"false"`
	Scan      *LibraryScan    `json:"scan,omitempty" required:"false" doc:"the scan under way or asked for; left out when none"`
}

// LibrariesOut is every library, and the media service's folders.
type LibrariesOut struct {
	Enabled   bool      `json:"enabled" doc:"media libraries are set up on this install (MEDIA_URL)"`
	Libraries []Library `json:"libraries" nullable:"false"`
	// The media service's own folders (MEDIA_LIBRARIES): library folders
	// must be inside them. Empty when the media service didn't answer.
	Folders []string `json:"folders" nullable:"false"`
	// Why the media service can't be reached, when it can't.
	MediaError *string `json:"media_error" required:"false" nullable:"true"`
}

// LibraryIn adds or changes a library. The kind can't change.
type LibraryIn struct {
	_     struct{} `additionalProperties:"true"`
	Name  string   `json:"name" minLength:"1" maxLength:"100"`
	Kind  string   `json:"kind,omitempty" required:"false" maxLength:"10" doc:"movies or shows; left out when changing a library"`
	Paths []string `json:"paths" minItems:"1" maxItems:"20"`
}

// LibraryProblem is a file that can't be read or matched.
type LibraryProblem struct {
	FileID   int64  `json:"file_id"`
	Path     string `json:"path" doc:"inside the library's folder"`
	Problem  string `json:"problem"`
	Title    string `json:"title" doc:"the title the name gave"`
	Year     *int32 `json:"year" required:"false" nullable:"true"`
	Season   *int32 `json:"season" required:"false" nullable:"true"`
	Episode  *int32 `json:"episode" required:"false" nullable:"true"`
	Readable bool   `json:"readable" doc:"false: the file itself can't be read"`
}

// LibraryProblemsOut is a library's problem files (the first 500).
type LibraryProblemsOut struct {
	Items []LibraryProblem `json:"items" nullable:"false"`
}

// LibraryMatchIn matches a file (and the files named like it) by hand.
type LibraryMatchIn struct {
	_      struct{} `additionalProperties:"true"`
	TMDBID int      `json:"tmdb_id" minimum:"1" maximum:"100000000"`
}

// LibraryMatchOut is how many files the match changed.
type LibraryMatchOut struct {
	Files int `json:"files"`
}

// LibraryCreated is a new library's id.
type LibraryCreated struct {
	ID int32 `json:"id"`
}
