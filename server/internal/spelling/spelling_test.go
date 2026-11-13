package spelling

import (
	"testing"
	"time"
)

var vocab = map[string]int{"kids": 700, "searching": 500, "treasure": 800, "map": 900, "amp": 2, "for": 5000, "the": 9000,
	"thieves": 300, "house": 1200, "sold": 400, "goonies": 5, "alien": 600, "with": 8000}

func TestFixesTyposSplitWordsAndRareLookalikes(t *testing.T) {
	if got := Correct("kids seraching fo r trasure with a amp", vocab); got != "kids searching for treasure with a map" {
		t.Fatal(got)
	}
	if got := Correct("alein", vocab); got != "alien" {
		t.Fatal(got)
	}
}

func TestLeavesGoodAndUnknownWordsAlone(t *testing.T) {
	for q, want := range map[string]string{"The Goonies": "The Goonies", "zxqwv": "zxqwv"} {
		if got := Correct(q, vocab); got != want {
			t.Errorf("%q: %q", q, got)
		}
	}
	if Correct("anything", map[string]int{}) != "anything" {
		t.Fatal("no vocabulary changed a word")
	}
}

func TestVocabularyCountsWordsSeenOftenEnough(t *testing.T) {
	counts := map[string]int{}
	for _, text := range []string{"A treasure map", "the treasure", "Treasure!", "rare word", "&amp; &amp; &amp;"} {
		AddWords(counts, text)
	}
	got := Keep(counts)
	if len(got) != 1 || got["treasure"] != 3 {
		t.Fatal(got)
	}
}

func TestALongQueryIsCorrectedQuickly(t *testing.T) {
	// The Python spent ~50 ms per described plot; a 200-character query of
	// unknown words is the worst case (two-letter edits of every word).
	q := "qwertyu asdfghj zxcvbnm poiuytr lkjhgfd mnbvcxz qazwsxe edcrfvt tgbyhnu ujmikol plokijn uhbygvt rfcedxw szaqwsx edcvfrt"
	d := NewDict(vocab)
	start := time.Now()
	d.Correct(q)
	first := time.Since(start)
	start = time.Now()
	d.Correct(q)
	again := time.Since(start)
	// Bounded the first time (~140 ms here, more under -race); from memory
	// after that.
	if first > 1500*time.Millisecond || again > 5*time.Millisecond {
		t.Fatalf("first %v, again %v", first, again)
	}
}
