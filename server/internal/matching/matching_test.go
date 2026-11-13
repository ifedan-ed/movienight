package matching

import "testing"

func TestTitleMatch(t *testing.T) {
	cases := []struct {
		q, title string
		want     float64
	}{
		{"goonies", "The Goonies", 1}, {"The Goonies", "Goonies", 1}, {"Amélie", "Amelie", 1},
		{"goonies", "The Goonies Show", 0.9}, {"goonies", "The Making of The Goonies", 0.85},
		{"goonies", "Treasure Planet", 0}, {"", "Anything", 0},
	}
	for _, c := range cases {
		if got := TitleMatch(c.q, c.title); got != c.want {
			t.Errorf("%q vs %q: %v, want %v", c.q, c.title, got, c.want)
		}
	}
}

func TestWordsAreCountedInEveryScript(t *testing.T) {
	cases := map[string]int{
		"don't know it": 3, "千と千尋": 2, "дети ищут карту сокровищ": 4, "Παιδιά με χάρτη θησαυρού": 4,
		"宝の地図を持った子供たちが家を救う": 9, "보물 지도를 가진 아이들": 4, "карта сокровищ": 2,
		"kids with a treasure map": 5, "  ": 0,
	}
	for q, want := range cases {
		if got := Words(q); got != want {
			t.Errorf("%q: %d words, want %d", q, got, want)
		}
	}
}

func TestPopularityBonusIsSmallAndCapped(t *testing.T) {
	if PopularityBonus(0) != 0 || PopularityBonus(9999) != 0.15 || PopularityBonus(1e7) != 0.15 || PopularityBonus(100) >= 0.15 {
		t.Fatal(PopularityBonus(0), PopularityBonus(9999), PopularityBonus(100))
	}
}
