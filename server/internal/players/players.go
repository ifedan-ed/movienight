// Package players is a person's outside players (VLC, Infuse, their own
// links) and their play choices: which players the Play sheet offers, the
// default, the subtitle language, and whether to ask "Finished it?".
//
// The one home of player settings: GET/PUT /me/players (rw/profile) and
// the Play sheet (package playback: GET/POST /title/.../play) both read
// them here, and playback fills a player's link with Fill and Template, so a
// saved link and a played link are judged and filled by the same code.
package players

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"movienight/server/internal/apierr"
	"movienight/server/internal/rw/schema"
	"movienight/server/internal/settings"
)

// The settings keys (the Python's, so an install's saved choices carry over).
const (
	CustomKey   = "players"
	DefaultKey  = "default_player"
	EnabledKey  = "enabled_players"
	SubLangKey  = "subtitle_language"
	AskKey      = "ask_finished"
	MaxCustom   = 20 // players of one's own (a limit the owner keeps: docs/limits.md)
	MaxEnabled  = 30 // players ticked
	MaxTemplate = 1000
)

// Builtin is one player Movie Night knows.
type Builtin struct{ ID, Name, Template string }

// Builtins, in the order the Play sheet lists them. No link back to Movie
// Night ({done}) for these: Infuse calls a film's link back when the next
// film replaces it, which pulled people out of the film they had just
// started. "Finished it?" is asked when they come back by themselves.
var Builtins = []Builtin{
	{"vlc", "VLC", "vlc-x-callback://x-callback-url/stream?url={url}&sub={sub}"},
	{"infuse", "Infuse", "infuse://x-callback-url/play?url={url}&sub={sub}&filename={filename}"},
	{"outplayer", "Outplayer", "outplayer://x-callback-url/play?url={url}&sub={sub}"},
	{"vlc_android", "VLC (Android)", "intent://{url_noscheme}#Intent;scheme={url_scheme};type=video/*;package=org.videolan.vlc;S.title={title};S.subtitles_location={sub};end"},
	{"jellyfin", "Jellyfin", "{web}"},
	// Swiftfin opens swiftfin://<server id>/<user id>/item/<item id> (1.5+),
	// Streamyfin streamyfin://items/page?id=<item id>; the official Jellyfin
	// apps have no such link, so "Jellyfin" opens the website.
	{"swiftfin", "Swiftfin", "{swiftfin}"},
	{"streamyfin", "Streamyfin", "streamyfin://items/page?id={jellyfin_id}"},
}

// JellyfinApps play through Jellyfin itself, which records those plays, so
// nobody is asked "Finished it?" about them (a list: the open-Play query
// takes it as an array).
var JellyfinApps = []string{"jellyfin", "swiftfin", "streamyfin"}

// IsJellyfinApp reports whether the player plays through Jellyfin.
func IsJellyfinApp(id string) bool {
	for _, a := range JellyfinApps {
		if a == id {
			return true
		}
	}
	return false
}

// Template is a player's link template: a built-in's, or the person's own
// (pl.Template); "" for an id no built-in has.
func Template(pl Player) string {
	if pl.Template != nil {
		return *pl.Template
	}
	for _, b := range Builtins {
		if b.ID == pl.ID {
			return b.Template
		}
	}
	return ""
}

var (
	placeholders = map[string]bool{"url": true, "url_raw": true, "url_noscheme": true, "url_scheme": true, "sub": true,
		"sub_raw": true, "title": true, "filename": true, "done": true, "web": true, "swiftfin": true, "jellyfin_id": true}
	blockedSchemes = map[string]bool{"javascript": true, "data": true, "vbscript": true, "file": true, "about": true, "blob": true}
	placeholderRe  = regexp.MustCompile(`\{([a-z_]+)\}`)
	idRe           = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)
	schemeRe       = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9+.\-]*):`)
	sample         = map[string]string{"url": "https%3A%2F%2Fexample.org%2Fv.mkv", "url_raw": "https://example.org/v.mkv",
		"url_noscheme": "example.org/v.mkv", "url_scheme": "https", "sub": "", "sub_raw": "", "title": "Title",
		"filename": "Title.mkv", "done": "", "web": "https://example.org/web", "swiftfin": "swiftfin://server/user/item/item",
		"jellyfin_id": "0123456789abcdef0123456789abcdef"}
)

// Fill puts values into a link's {placeholders} (unknown ones become "").
func Fill(template string, values map[string]string) string {
	return placeholderRe.ReplaceAllStringFunc(template, func(m string) string { return values[m[1:len(m)-1]] })
}

func invalid(msg string) error { return apierr.New(http.StatusUnprocessableEntity, msg) }

// CheckTemplate judges a link of one's own: known placeholders, the video
// somewhere, and an app's scheme once filled in (never javascript: and the
// like). nPlayer's is nplayer-{url_raw}, for example.
func CheckTemplate(template string) (string, error) {
	t := strings.TrimSpace(template)
	var unknown []string
	for _, m := range placeholderRe.FindAllStringSubmatch(t, -1) {
		if !placeholders[m[1]] {
			unknown = append(unknown, m[1])
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return "", invalid(fmt.Sprintf("Unknown placeholder: {%s}", unknown[0]))
	}
	if !strings.Contains(t, "{url") && !strings.Contains(t, "{web}") {
		return "", invalid("The link needs {url} (the video) somewhere")
	}
	// Characters, not bytes: the limit is said in characters, and a link
	// with non-ASCII text must not be refused short of it.
	if utf8.RuneCountInString(t) > MaxTemplate {
		return "", invalid("A player link can be at most 1,000 characters")
	}
	m := schemeRe.FindStringSubmatch(Fill(t, sample))
	if m == nil || blockedSchemes[strings.ToLower(m[1])] {
		return "", invalid("A player link starts with the app's scheme, like vlc:// or infuse://")
	}
	return t, nil
}

// Custom is a saved player of one's own.
type Custom struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Template string `json:"template"`
}

// Settings is the person's players and choices (PlayerSettingsOut).
type Settings = schema.PlayerSettingsOut

// Player is one player (schema.Player).
type Player = schema.Player

// Service reads and saves the choices.
type Service struct{ s *settings.Service }

// New makes the service.
func New(s *settings.Service) *Service { return &Service{s: s} }

// Get is the person's players and choices: every built-in, then theirs;
// all offered unless they chose; the default one of those offered.
func (p *Service) Get(ctx context.Context, userID int32) (Settings, error) {
	var custom []Custom
	if _, err := p.s.User(ctx, userID, CustomKey, &custom); err != nil {
		return Settings{}, err
	}
	var players []Player
	for _, b := range Builtins {
		players = append(players, Player{ID: b.ID, Name: b.Name, Builtin: true})
	}
	for _, c := range custom {
		t := c.Template
		players = append(players, Player{ID: c.ID, Name: c.Name, Template: &t})
	}
	known := map[string]bool{}
	var ids []string
	for _, pl := range players {
		known[pl.ID] = true
		ids = append(ids, pl.ID)
	}
	var chosen []string
	if _, err := p.s.User(ctx, userID, EnabledKey, &chosen); err != nil {
		return Settings{}, err
	}
	var enabled []string
	for _, id := range chosen {
		if known[id] {
			enabled = append(enabled, id)
		}
	}
	if len(enabled) == 0 {
		enabled = ids // none chosen (or none left): every player
	}
	on := map[string]bool{}
	for _, id := range enabled {
		on[id] = true
	}
	for i := range players {
		players[i].Enabled = on[players[i].ID]
	}
	def := "vlc"
	if _, err := p.s.User(ctx, userID, DefaultKey, &def); err != nil {
		return Settings{}, err
	}
	if !on[def] {
		def = enabled[0]
	}
	lang := "eng"
	if _, err := p.s.User(ctx, userID, SubLangKey, &lang); err != nil {
		return Settings{}, err
	}
	ask, err := p.Asks(ctx, userID)
	if err != nil {
		return Settings{}, err
	}
	return Settings{Players: players, DefaultPlayer: def, SubtitleLanguage: &lang, AskFinished: ask}, nil
}

// Asks is whether to ask "Finished it?" (on unless switched off).
func (p *Service) Asks(ctx context.Context, userID int32) (bool, error) {
	var ask any = true
	if _, err := p.s.User(ctx, userID, AskKey, &ask); err != nil {
		return false, err
	}
	return ask != false, nil
}

// Choice is one custom player as typed.
type Choice struct{ Name, Template string }

// Save keeps the choices (nil means "leave as it is", except custom, which
// is always the full list) and returns them as Get does. Every link is
// checked before anything is written, so a bad one changes nothing (the
// Python saved the players before checking the rest).
func (p *Service) Save(ctx context.Context, userID int32, custom []Choice, def *string, lang *string, enabled []string, ask *bool) (Settings, error) {
	for _, id := range enabled {
		// Checked here, not by the schema: Huma's tags cannot put a pattern
		// on the items of an optional list.
		if !idRe.MatchString(id) {
			return Settings{}, invalid("Enabled: each player id is lower-case letters, digits and _.")
		}
	}
	clean := make([]Custom, 0, len(custom))
	for i, c := range custom {
		if i == MaxCustom {
			break
		}
		name := strings.TrimSpace(c.Name)
		if name == "" {
			return Settings{}, invalid("Each player needs a name")
		}
		t, err := CheckTemplate(c.Template)
		if err != nil {
			return Settings{}, err
		}
		clean = append(clean, Custom{ID: fmt.Sprintf("custom%d", i+1), Name: name, Template: t})
	}
	if err := p.s.SetUser(ctx, userID, CustomKey, clean); err != nil {
		return Settings{}, err
	}
	if def != nil && *def != "" {
		if err := p.s.SetUser(ctx, userID, DefaultKey, *def); err != nil {
			return Settings{}, err
		}
	}
	if enabled != nil {
		if len(enabled) > MaxEnabled {
			enabled = enabled[:MaxEnabled]
		}
		if err := p.s.SetUser(ctx, userID, EnabledKey, enabled); err != nil {
			return Settings{}, err
		}
	}
	if lang != nil {
		l := strings.ToLower(strings.TrimSpace(*lang))
		// Cut by characters: a byte cut could split a letter in two.
		if r := []rune(l); len(r) > 3 {
			l = string(r[:3])
		}
		if strings.IndexFunc(l, func(r rune) bool { return !unicode.IsLetter(r) }) >= 0 || l == "" {
			l = "" // no letters: no preferred language
		}
		if err := p.s.SetUser(ctx, userID, SubLangKey, l); err != nil {
			return Settings{}, err
		}
	}
	if ask != nil {
		if err := p.s.SetUser(ctx, userID, AskKey, *ask); err != nil {
			return Settings{}, err
		}
	}
	return p.Get(ctx, userID)
}
