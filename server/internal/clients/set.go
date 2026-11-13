package clients

import (
	"log/slog"
	"net/http"

	"github.com/valkey-io/valkey-go"

	"movienight/server/internal/config"
)

// Set is every outside-service client, built once from the settings and
// shared by the API and the worker.
type Set struct {
	TMDB     *TMDB
	Jellyfin *Jellyfin
	Seerr    *Seerr
	MDBList  *MDBList
	Meili    *Meili
	// AI writes "Picked for you" rows and digests (LITELLM_MODEL);
	// SearchAI reads described plots in search (SEARCH_AI_MODEL). Same
	// endpoint and key; either may be off (Enabled() false).
	AI       *LiteLLM
	SearchAI *LiteLLM

	upstream *http.Client
}

// NewSet builds the clients.
func NewSet(cfg *config.Config, kv valkey.Client, log *slog.Logger) *Set {
	up, img := Upstream(), Images()
	return &Set{
		TMDB:     NewTMDB(up, TMDBAPI, cfg.TMDBAPIKey, cfg.TMDBLanguage, cfg.WatchRegion, cfg.HideAdultTitles),
		Jellyfin: NewJellyfin(up, img, cfg.JellyfinBaseURL, cfg.JellyfinPublicURL, cfg.JellyfinAPIKey, cfg.DryRun),
		Seerr:    NewSeerr(up, cfg.SeerrBaseURL, cfg.SeerrAPIKey, cfg.DryRun),
		MDBList:  NewMDBList(up, MDBListAPI, cfg.MDBListAPIKey, kv, log),
		Meili:    NewMeili(up, cfg.MeiliBaseURL, cfg.MeiliAPIKey, cfg.MeiliEmbedderModel, cfg.SearchMaxHits, log),
		AI:       NewLiteLLM(up, cfg.LiteLLMBaseURL, cfg.LiteLLMAPIKey, cfg.LiteLLMModel, cfg.LiteLLMMaxTokens, log),
		SearchAI: NewLiteLLM(up, cfg.LiteLLMBaseURL, cfg.LiteLLMAPIKey, cfg.SearchAIModel, cfg.SearchAIMaxTokens, log),
		upstream: up,
	}
}

// Upstream is the shared HTTP client (the OIDC flow uses it too).
func (s *Set) Upstream() *http.Client { return s.upstream }

// Close drops idle connections at shutdown.
func (s *Set) Close() {
	s.upstream.CloseIdleConnections()
	s.Jellyfin.images.CloseIdleConnections()
}
