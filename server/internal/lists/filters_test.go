package lists

import (
	"maps"
	"testing"
)

func TestSmartFiltersToTMDB(t *testing.T) {
	f := CleanFilters(map[string]any{"media": "tv", "genre": "18", "year_from": "1990", "min_rating": "7",
		"sort": "primary_release_date.desc", "language": "ko", "provider": "x", "nonsense": 1})
	got := TMDBFilters(f, "US")
	want := map[string]string{"with_genres": "18", "first_air_date.gte": "1990-01-01", "vote_average.gte": "7",
		"vote_count.gte": "100", "with_original_language": "ko", "sort_by": "first_air_date.desc"}
	if !maps.Equal(got, want) {
		t.Fatal(got)
	}
	if f := CleanFilters(map[string]any{"media": "nope", "sort": "random"}); len(f) != 1 || f["media"] != "movie" {
		t.Fatal(f)
	}
}
