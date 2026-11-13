package config

import (
	"fmt"
	"os"
	"strings"
)

// JobSettings are the settings of the worker's scheduled jobs, the digest
// emails and the AI rows. They sit in
// their own file so the API's settings and these can grow apart. The API
// reads a few of them too: CATALOG_ENABLED (search), the AI rows' freshness
// (Home asks for a rebuild) and EmailEnabled (GET /me).
type JobSettings struct {
	// RUN_JOBS: false starts the worker without its schedule (the queued
	// work, webhook syncs and AI rows, still runs). Default true.
	RunJobs bool

	// IMDB_RATINGS_ENABLED: the nightly load of IMDb's ratings file.
	ImdbRatingsEnabled bool
	// CATALOG_ENABLED and the vote floors: the weekly search catalog build,
	// and whether search reads the "catalog" index (every rated TMDB title).
	CatalogEnabled       bool
	CatalogMovieMinVotes int
	CatalogTVMinVotes    int

	// AI rows ("Picked for you"): rebuilt when older than
	// AI_ROWS_CACHE_TTL_HOURS or after AI_REFRESH_AFTER_WATCHES new watches;
	// the prompt holds at most AI_PROMPT_TITLES titles and ratings and
	// AI_PROMPT_COMMENTS comments of AI_PROMPT_COMMENT_CHARS characters.
	AIRowsCacheTTLHours   int
	AIRefreshAfterWatches int
	AIPromptTitles        int
	AIPromptComments      int
	AIPromptCommentChars  int

	// MAIL_SENDER: "smtp" (default) or "smtp2go" (SMTP2GO's HTTP API, its key
	// read from the file SMTP2GO_API_KEY_FILE at each send). Anything else is
	// an error, not "smtp".
	MailSender        string
	SMTP2GOAPIKeyFile string
	SMTPHost          string
	SMTPPort          int // default 587
	SMTPUser          string
	SMTPPassword      string
	SMTPFrom          string // default "Movie Night <movienight@example.com>", as the Python
	SMTPStartTLS      bool   // default true
	// DIGEST_SEND_TRIES: the schedule's tries per digest and period (3).
	DigestSendTries int

	// KODI_ADDON_DIR and JELLYFIN_PLUGIN_DIR: the Kodi add-on's folder
	// (kodi/service.movienight) and the Jellyfin plugin's (jellyfin-plugin/,
	// with the built DLL in out/), served at /kodi/ and /jellyfin/. The image
	// sets both. Unset: that download answers 503 naming the setting.
	KodiAddonDir      string
	JellyfinPluginDir string
}

// EmailEnabled reports whether digests can be sent: SMTP_HOST for SMTP, the
// key file for SMTP2GO (the Python's email_enabled).
func (j JobSettings) EmailEnabled() bool {
	if j.MailSender == "smtp2go" {
		return j.SMTP2GOAPIKeyFile != ""
	}
	return j.SMTPHost != ""
}

func (r *reader) jobSettings() JobSettings {
	j := JobSettings{}
	j.RunJobs = r.boolean("RUN_JOBS", true)

	j.ImdbRatingsEnabled = r.boolean("IMDB_RATINGS_ENABLED", true)
	j.CatalogEnabled = r.boolean("CATALOG_ENABLED", true)
	j.CatalogMovieMinVotes = r.positive("CATALOG_MOVIE_MIN_VOTES", 5)
	j.CatalogTVMinVotes = r.positive("CATALOG_TV_MIN_VOTES", 5)

	j.AIRowsCacheTTLHours = r.positive("AI_ROWS_CACHE_TTL_HOURS", 24)
	j.AIRefreshAfterWatches = r.positive("AI_REFRESH_AFTER_WATCHES", 3)
	j.AIPromptTitles = r.positive("AI_PROMPT_TITLES", 60)
	j.AIPromptComments = r.positive("AI_PROMPT_COMMENTS", 20)
	j.AIPromptCommentChars = r.positive("AI_PROMPT_COMMENT_CHARS", 300)

	j.MailSender = strings.ToLower(r.withDefault("MAIL_SENDER", "smtp"))
	if j.MailSender != "smtp" && j.MailSender != "smtp2go" {
		r.fail(fmt.Sprintf("MAIL_SENDER must be smtp or smtp2go, not %q", j.MailSender))
	}
	j.SMTP2GOAPIKeyFile = r.optional("SMTP2GO_API_KEY_FILE")
	j.SMTPHost = r.optional("SMTP_HOST")
	j.SMTPPort = r.positive("SMTP_PORT", 587)
	j.SMTPUser = r.optional("SMTP_USER")
	j.SMTPPassword = r.optional("SMTP_PASSWORD")
	j.SMTPFrom = r.withDefault("SMTP_FROM", "Movie Night <movienight@example.com>")
	j.SMTPStartTLS = r.boolean("SMTP_STARTTLS", true)
	j.DigestSendTries = r.positive("DIGEST_SEND_TRIES", 3)
	j.KodiAddonDir = r.optional("KODI_ADDON_DIR")
	j.JellyfinPluginDir = r.optional("JELLYFIN_PLUGIN_DIR")
	// A sender named but not set up is a typo, not "no email".
	if j.MailSender == "smtp2go" && j.SMTP2GOAPIKeyFile == "" {
		r.fail("MAIL_SENDER is smtp2go but SMTP2GO_API_KEY_FILE is not set")
	}
	if j.SMTP2GOAPIKeyFile != "" {
		if why := keyFileProblem(j.SMTP2GOAPIKeyFile); why != "" {
			r.fail("SMTP2GO_API_KEY_FILE " + why)
		}
	}
	return j
}

// keyFileProblem says why a key file can't be used ("" when it can): read
// at start-up, so a file that isn't mounted, or that the container's user
// may not read (the image runs as a non-root user), stops the start instead
// of failing the first digest days later. The key itself is not kept here;
// the mailer reads it again at each send.
func keyFileProblem(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsPermission(err) {
			return fmt.Sprintf("can't be read by this user (uid %d): give the container that file's owner (compose `user:`)", os.Getuid())
		}
		return "can't be read: " + err.Error()
	}
	if strings.TrimSpace(string(raw)) == "" {
		return "is empty"
	}
	return ""
}
