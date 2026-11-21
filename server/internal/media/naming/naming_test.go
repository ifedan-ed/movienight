package naming

import "testing"

func TestFilms(t *testing.T) {
	cases := []struct {
		path  string
		title string
		year  int
		tmdb  int
		imdb  string
		skip  bool
	}{
		// Radarr and Jellyfin layouts.
		{"Inception (2010)/Inception (2010).mkv", "Inception", 2010, 0, "", false},
		{"Inception (2010) [tmdbid-27205]/Inception (2010) - Bluray-1080p.mkv", "Inception", 2010, 27205, "", false},
		{"Fight Club (1999) {tmdb-550}/Fight Club (1999).mp4", "Fight Club", 1999, 550, "", false},
		{"Fight Club (1999) [imdbid-tt0137523]/Fight Club.mkv", "Fight Club", 1999, 0, "tt0137523", false},
		{"The Matrix (1999) {imdb-tt0133093}/movie.mkv", "The Matrix", 1999, 0, "tt0133093", false},
		{"Arrival (2016) [tmdb=329865]/Arrival.mkv", "Arrival", 2016, 329865, "", false},
		// The file's own id wins over its folder's.
		{"Some Film (2001) [tmdbid-1]/Some Film (2001) [tmdbid-2].mkv", "Some Film", 2001, 2, "", false},
		// Scene names.
		{"Inception.2010.1080p.BluRay.x264-SPARKS.mkv", "Inception", 2010, 0, "", false},
		{"The.Dark.Knight.2008.2160p.UHD.BluRay.REMUX.HDR.HEVC.Atmos-EPSiLON.mkv", "The Dark Knight", 2008, 0, "", false},
		{"Mad.Max.Fury.Road.2015.WEB-DL.DDP5.1.H.264-NTb.mkv", "Mad Max Fury Road", 2015, 0, "", false},
		{"Spirited_Away_2001_720p.mp4", "Spirited Away", 2001, 0, "", false},
		{"Movies/Amelie.2001.tt0211915.mkv", "Amelie", 2001, 0, "tt0211915", false},
		// Years in titles.
		{"Blade Runner 2049 (2017)/Blade Runner 2049 (2017).mkv", "Blade Runner 2049", 2017, 0, "", false},
		{"1917 (2019)/1917.mkv", "1917", 2019, 0, "", false},
		{"2001 A Space Odyssey (1968).mkv", "2001 A Space Odyssey", 1968, 0, "", false},
		{"2012.mkv", "2012", 0, 0, "", false},
		{"Blade.Runner.2049.2017.1080p.mkv", "Blade Runner 2049", 2017, 0, "", false},
		// A flat folder with no year, and a title with punctuation.
		{"Amélie.mkv", "Amélie", 0, 0, "", false},
		{"Mission - Impossible (1996).mkv", "Mission - Impossible", 1996, 0, "", false},
		{"Mr. Nobody (2009).mkv", "Mr. Nobody", 2009, 0, "", false},
		// A folder that names the film, with a file that does not.
		{"Heat (1995)/1080p.mkv", "Heat", 1995, 0, "", false},
		// Extras are skipped.
		{"Heat (1995)/Heat-trailer.mkv", "Heat", 1995, 0, "", true},
		{"Heat (1995)/Extras/Making of.mkv", "Heat", 1995, 0, "", true},
		{"Heat (1995)/Featurettes/Interview.mkv", "Heat", 1995, 0, "", true},
		{"Heat.1995.1080p/sample.mkv", "Heat", 1995, 0, "", true},
		{"Heat (1995)/Heat (1995)-sample.mkv", "Heat", 1995, 0, "", true},
		// Brackets that are not ids are dropped.
		{"[Group] Your Name (2016) [1080p].mkv", "Your Name", 2016, 0, "", false},
	}
	for _, c := range cases {
		p := Parse(c.path, Movies)
		if c.skip {
			if !p.Skip {
				t.Errorf("%s: not skipped", c.path)
			}
			continue
		}
		if p.Title != c.title || p.Year != c.year || p.TMDB != c.tmdb || p.IMDb != c.imdb || p.Skip != c.skip {
			t.Errorf("%s: got %q %d tmdb %d imdb %q skip %v; want %q %d tmdb %d imdb %q skip %v",
				c.path, p.Title, p.Year, p.TMDB, p.IMDb, p.Skip, c.title, c.year, c.tmdb, c.imdb, c.skip)
		}
	}
}

func TestEpisodes(t *testing.T) {
	cases := []struct {
		path                 string
		title                string
		year, tmdb, tvdb     int
		season, episode, end int
	}{
		// Sonarr's layout.
		{"Breaking Bad (2008) [tvdbid-81189]/Season 01/Breaking Bad - S01E01 - Pilot.mkv", "Breaking Bad", 2008, 0, 81189, 1, 1, 0},
		{"Breaking Bad (2008) {tmdb-1396}/Season 5/Breaking Bad - S05E16 - Felina.mkv", "Breaking Bad", 2008, 1396, 0, 5, 16, 0},
		{"The Office (US)/Season 02/The Office (US) - S02E01E02 - The Dundies.mkv", "The Office (US)", 0, 0, 0, 2, 1, 2},
		{"Lost/Season 1/Lost - S01E01-E02.mkv", "Lost", 0, 0, 0, 1, 1, 2},
		{"Lost/Season 1/Lost.S01E03-04.mkv", "Lost", 0, 0, 0, 1, 3, 4},
		// Scene names, with and without a show folder.
		{"Game.of.Thrones.S08E06.1080p.WEB.H264-MEMENTO.mkv", "Game of Thrones", 0, 0, 0, 8, 6, 0},
		{"Severance.2022.S02E10.2160p.ATVP.WEB-DL.mkv", "Severance", 2022, 0, 0, 2, 10, 0},
		{"Severance/Severance.S02E10.2160p.ATVP.WEB-DL-GRP/Severance.S02E10.mkv", "Severance", 0, 0, 0, 2, 10, 0},
		{"Doctor Who (2005)/Season 1/Doctor.Who.2005.1x01.Rose.avi", "Doctor Who", 2005, 0, 0, 1, 1, 0},
		{"Show/s1e2.mp4", "Show", 0, 0, 0, 1, 2, 0},
		{"Show/S01.E03.mkv", "Show", 0, 0, 0, 1, 3, 0},
		// The season from the folder when the name has only the episode.
		{"Planet Earth/Season 2/Episode 3.mkv", "Planet Earth", 0, 0, 0, 2, 3, 0},
		{"Planet Earth/Specials/Planet Earth - S00E01 - Making Of.mkv", "Planet Earth", 0, 0, 0, 0, 1, 0},
		// A library pointed at one show: the season folder is the first one.
		{"Season 03/Show Name - S03E04.mkv", "Show Name", 0, 0, 0, 3, 4, 0},
		// No episode number anywhere.
		{"Some Show/Behind it all.mkv", "Some Show", 0, 0, 0, 0, 0, 0},
	}
	for _, c := range cases {
		p := Parse(c.path, Shows)
		if p.Title != c.title || p.Year != c.year || p.TMDB != c.tmdb || p.TVDB != c.tvdb ||
			p.Season != c.season || p.Episode != c.episode || p.EpisodeEnd != c.end {
			t.Errorf("%s: got %q %d tmdb %d tvdb %d S%dE%d-%d; want %q %d tmdb %d tvdb %d S%dE%d-%d",
				c.path, p.Title, p.Year, p.TMDB, p.TVDB, p.Season, p.Episode, p.EpisodeEnd,
				c.title, c.year, c.tmdb, c.tvdb, c.season, c.episode, c.end)
		}
	}
}

func TestEpisodeExtrasSkipped(t *testing.T) {
	if p := Parse("Show/Season 1/Show - S01E01-sample.mkv", Shows); !p.Skip {
		t.Error("a sample was not skipped")
	}
	if p := Parse("Show/Extras/Show - Interview.mkv", Shows); !p.Skip {
		t.Error("an extras folder was not skipped")
	}
}
