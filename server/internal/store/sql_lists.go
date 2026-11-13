package store

// The SQL of the hot list and Home-row queries, for the EXPLAIN tests that
// check they can use their indexes (sqlc keeps each query's text
// unexported). Not generated: sqlc leaves files it did not write alone.
var (
	ListItemsPageSQL        = listItemsPage
	WatchlistPageSQL        = watchlistPage
	FavoritesPageSQL        = favoritesPage
	RatingsPageSQL          = ratingsPage
	UnwatchedLibraryPageSQL = unwatchedLibraryPage
	RecentSeedsSQL          = recentSeeds
)
