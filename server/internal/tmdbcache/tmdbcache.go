// Package tmdbcache is TMDB's single-title answers, kept in the shared
// cache (package cache): a title's full details and a show's season, each
// for 6 hours, as the Python app kept them. A
// title TMDB does not know (nil) is kept too, so an unknown id does not
// cost a TMDB call per view. Shared by the title page, the season page and
// "Up next".
package tmdbcache

import (
	"context"
	"fmt"
	"time"

	"movienight/server/internal/cache"
	"movienight/server/internal/clients"
)

// TTL is how long a title's details and a season stay fresh.
const TTL = 6 * time.Hour

// Details is TMDB's full details of a title (nil: TMDB has no such title).
func Details(ctx context.Context, c *cache.Cache, tmdb *clients.TMDB, media string, id int) (clients.JSON, error) {
	return cache.Cached(ctx, c, fmt.Sprintf("detail:%s:%d", media, id), cache.Options{TTL: TTL},
		func(ctx context.Context) (clients.JSON, error) { return tmdb.Details(ctx, media, id) })
}

// Season is TMDB's season of a show (nil: no such season).
func Season(ctx context.Context, c *cache.Cache, tmdb *clients.TMDB, showID, number int) (clients.JSON, error) {
	return cache.Cached(ctx, c, fmt.Sprintf("season:%d:%d", showID, number), cache.Options{TTL: TTL},
		func(ctx context.Context) (clients.JSON, error) { return tmdb.Season(ctx, showID, number) })
}
