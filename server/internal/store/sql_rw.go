package store

// The SQL of the hot read-route queries, for the EXPLAIN tests that check
// they can use their indexes (sqlc keeps each query's text unexported).
// Not generated: sqlc leaves files it did not write alone.
var (
	StateForTitlesSQL = stateForTitles
	TitlesByKeysSQL   = titlesByKeys
)
