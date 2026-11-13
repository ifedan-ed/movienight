package homecfg

import "testing"

func TestMergeDropsUnknownAndRepeatsAndAppendsNewRowsOff(t *testing.T) {
	avail := []string{"continue", "recent", "list:5"}
	got := Merge([]Row{{"recent", true}, {"list:9", true}, {"recent", false}}, "for_you", avail)
	if len(got) != 3 || got[0] != (Row{"recent", true}) || got[1] != (Row{"continue", false}) || got[2] != (Row{"list:5", false}) {
		t.Fatal(got)
	}
	// Nothing saved: the tab's defaults, on.
	def := Merge(nil, "movies", Catalog("movies", nil))
	if def[0] != (Row{"continue", true}) || def[len(def)-1].On {
		t.Fatal(def)
	}
}
