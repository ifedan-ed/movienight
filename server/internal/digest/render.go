package digest

import (
	"fmt"
	"html"
	"math"
	"strings"
	"time"

	"movienight/server/internal/titles"
)

// The email's look. Email clients need tables and inline styles; nothing in
// it loads or runs anything but TMDB's posters.
const img = "https://image.tmdb.org/t/p/w185"

var c = struct{ bg, card, ink, soft, line, accent string }{
	"#f4f1ec", "#ffffff", "#1d1a16", "#6b635a", "#e6e0d7", "#b5452b",
}

const sans = "Helvetica,Arial,sans-serif"

func e(s string) string { return html.EscapeString(s) }

func (s *Service) link(key *string) string {
	if key == nil {
		return s.cfg.PublicURL
	}
	media, id, ok := titles.Split(*key)
	if !ok {
		return s.cfg.PublicURL
	}
	return fmt.Sprintf("%s/title/%s/%d", s.cfg.PublicURL, media, id)
}

// UnsubscribeURL opens Profile → Emails on the website and turns this kind
// off there (also the email's List-Unsubscribe header).
func (s *Service) UnsubscribeURL(kind string) string {
	return s.cfg.PublicURL + "/settings/emails?off=" + kind
}

func poster(path *string, w int) string {
	h := int(float64(w) * 1.5)
	if path == nil {
		return fmt.Sprintf(`<div style="width:%dpx;height:%dpx;background:%s;border-radius:6px"></div>`, w, h, c.line)
	}
	return fmt.Sprintf(`<img src="%s%s" width="%d" height="%d" alt="" style="display:block;border-radius:6px;border:0">`, img, e(*path), w, h)
}

func stars(n *int32) string {
	if n == nil || *n == 0 {
		return ""
	}
	return fmt.Sprintf(`<span style="color:%s;font-weight:600">★ %d/10</span>`, c.accent, *n)
}

// more: "…and 3 more shows": a list cut short says so.
func more(n int, what string) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(`<div style="font:13px/1.5 %s;color:%s;margin:6px 0 4px">…and %d more %s</div>`, sans, c.soft, n, e(what))
}

func section(title, body string) string {
	return fmt.Sprintf(`<tr><td style="padding:26px 28px 6px;font:600 12px/1.3 %s;letter-spacing:.12em;`+
		`text-transform:uppercase;color:%s">%s</td></tr><tr><td style="padding:0 28px">%s</td></tr>`, sans, c.soft, e(title), body)
}

// gridItem is what a poster grid shows of one title.
type gridItem struct {
	key     *string
	title   string
	poster  *string
	caption string // already escaped
}

// grid lays posters out four to a row.
func (s *Service) grid(items []gridItem) string {
	var b strings.Builder
	b.WriteString(`<table role="presentation" cellpadding="0" cellspacing="0" width="100%">`)
	for n := 0; n < len(items); n += 4 {
		b.WriteString("<tr>")
		for _, it := range items[n:min(n+4, len(items))] {
			fmt.Fprintf(&b, `<td valign="top" width="25%%" style="padding:6px 8px 10px 0"><a href="%s" style="text-decoration:none;color:%s">`+
				`%s<div style="font:600 13px/1.3 %s;margin-top:6px">%s</div>`+
				`<div style="font:12px/1.3 %s;color:%s">%s</div></a></td>`,
				s.link(it.key), c.ink, poster(it.poster, 96), sans, e(it.title), sans, c.soft, it.caption)
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</table>")
	return b.String()
}

func yearText(y *int) string {
	if y == nil {
		return ""
	}
	return fmt.Sprint(*y)
}

// months draws twelve bars, one a month, as table cells (email clients draw
// no charts).
func months(ms []Month) string {
	top := 0
	for _, m := range ms {
		top = max(top, m.Films+m.Episodes)
	}
	if top == 0 {
		top = 1
	}
	var b strings.Builder
	b.WriteString(`<table role="presentation" cellpadding="0" cellspacing="0" width="100%"><tr>`)
	for i, m := range ms {
		n := m.Films + m.Episodes
		h, color, label := 2, c.line, ""
		if n > 0 {
			h, color, label = max(2, int(math.Round(70*float64(n)/float64(top)))), c.accent, fmt.Sprint(n)
		}
		fmt.Fprintf(&b, `<td valign="bottom" align="center" style="padding:0 2px;font:11px/1.2 %s;color:%s">`+
			`<div style="font-weight:600;color:%s;margin-bottom:3px">%s</div>`+
			`<div style="height:%dpx;background:%s;border-radius:3px 3px 0 0"></div>`+
			`<div style="margin-top:4px">%s</div></td>`, sans, c.soft, c.ink, label, h, color, time.Month(i + 1).String()[:1])
	}
	b.WriteString("</tr></table>")
	return b.String()
}

// row is one film or show line: poster, name, and the lines under it
// (already HTML).
func (s *Service) row(key *string, posterPath *string, name, under string) string {
	return fmt.Sprintf(`<tr><td valign="top" width="64" style="padding:10px 14px 10px 0"><a href="%s">%s</a></td>`+
		`<td valign="top" style="padding:10px 0;border-bottom:1px solid %s;font:14px/1.45 %s;color:%s">`+
		`<a href="%s" style="color:%s;text-decoration:none;font-weight:600;font-size:15px">%s</a>%s</td></tr>`,
		s.link(key), poster(posterPath, 60), c.line, sans, c.ink, s.link(key), c.ink, e(name), under)
}

func table(rows string) string {
	return `<table role="presentation" cellpadding="0" cellspacing="0" width="100%">` + rows + `</table>`
}

func (s *Service) yearSections(d Data, w Words) []string {
	y, t := d.Year, d.Totals
	var parts []string
	lines := []string{strings.Join([]string{plural(t.Films, "film"), plural(t.Episodes, "episode"), fmt.Sprintf("about %d hours", t.Hours)}, " · ")}
	if y.Busiest != nil {
		lines = append(lines, "Busiest month: <b>"+e(*y.Busiest)+"</b>")
	}
	if len(y.Genres) > 0 {
		gs := make([]string, len(y.Genres))
		for i, g := range y.Genres {
			gs[i] = e(g)
		}
		lines = append(lines, "Favourite genres: "+strings.Join(gs, ", "))
	}
	var bits []string
	if y.Rated > 0 {
		bits = append(bits, fmt.Sprintf("%d rated", y.Rated))
	}
	if y.Loved > 0 {
		bits = append(bits, plural(y.Loved, "new favourite"))
	}
	if len(bits) > 0 {
		lines = append(lines, strings.Join(bits, " · "))
	}
	parts = append(parts, section("The year in numbers",
		fmt.Sprintf(`<div style="font:14px/1.7 %s;color:%s">%s</div>`, sans, c.ink, strings.Join(lines, "<br>"))))
	busy := false
	for _, m := range y.Months {
		busy = busy || m.Films+m.Episodes > 0
	}
	if busy {
		parts = append(parts, section("Month by month", months(y.Months)))
	}
	if len(y.BestFilms) > 0 {
		items := make([]gridItem, 0, len(y.BestFilms))
		var notes strings.Builder
		for _, f := range y.BestFilms {
			items = append(items, gridItem{f.Key, f.Title.Title, f.Poster, fmt.Sprintf("★ %d/10", *f.Rating)})
			if n := w.FilmNotes[f.Title.Title]; n != "" {
				fmt.Fprintf(&notes, `<div style="font:13px/1.5 %s;color:%s;margin:4px 0"><b>%s</b>: %s</div>`, sans, c.ink, e(f.Title.Title), e(n))
			}
		}
		parts = append(parts, section("Your best films", s.grid(items)+notes.String()))
	}
	if len(y.TopShows) > 0 {
		var rows strings.Builder
		for _, sh := range y.TopShows {
			rows.WriteString(s.row(sh.Key, sh.Poster, sh.Title.Title,
				fmt.Sprintf(`<br><span style="color:%s">%s</span>`, c.soft, plural(sh.Episodes, "episode"))))
		}
		parts = append(parts, section("Most watched shows", table(rows.String())))
	}
	var ends []gridItem
	for _, end := range []struct {
		label string
		f     *Film
	}{{"First film of the year", y.FirstFilm}, {"Last film of the year", y.LastFilm}} {
		if end.f != nil {
			ends = append(ends, gridItem{end.f.Key, end.f.Title.Title, end.f.Poster, e(end.label + " · " + end.f.WatchedOn)})
		}
	}
	if len(ends) > 0 {
		parts = append(parts, section("First and last", s.grid(ends)))
	}
	return parts
}

var headings = map[string]string{"week": "Your week in film", "month": "Your month in review", "year": "Your year in review"}

// Render makes the email's HTML and its plain-text part.
func (s *Service) Render(d Data, w Words) (page, text string) {
	t := d.Totals
	var stat []string
	if t.Films > 0 {
		stat = append(stat, plural(t.Films, "film"))
	}
	if t.Episodes > 0 {
		stat = append(stat, plural(t.Episodes, "episode"))
	}
	if t.Hours > 0 {
		stat = append(stat, fmt.Sprintf("%d h", t.Hours))
	}
	if len(stat) == 0 {
		stat = []string{"nothing watched"}
	}
	heading := headings[d.Kind]
	parts := []string{
		fmt.Sprintf(`<tr><td style="padding:28px 28px 0;font:600 12px/1 %s;letter-spacing:.16em;text-transform:uppercase;color:%s">Movie Night</td></tr>`, sans, c.accent),
		fmt.Sprintf(`<tr><td style="padding:8px 28px 0;font:700 28px/1.2 Georgia,'Times New Roman',serif;color:%s">%s</td></tr>`, c.ink, heading),
		fmt.Sprintf(`<tr><td style="padding:4px 28px 0;font:15px/1.4 %s;color:%s">%s · %s</td></tr>`, sans, c.soft, e(d.Label), strings.Join(stat, " · ")),
		fmt.Sprintf(`<tr><td style="padding:18px 28px 0;font:16px/1.6 Georgia,'Times New Roman',serif;color:%s">%s</td></tr>`, c.ink, e(w.Intro)),
	}
	if d.Kind == "year" && d.Year != nil {
		parts = append(parts, s.yearSections(d, w)...)
	}
	if len(d.Films) > 0 && d.Kind != "year" {
		var rows strings.Builder
		for _, f := range d.Films {
			under := ""
			if f.Year != nil {
				under += fmt.Sprintf(` <span style="color:%s">(%d)</span>`, c.soft, *f.Year)
			}
			under += fmt.Sprintf(`<br><span style="color:%s">%s</span> %s`, c.soft, e(f.WatchedOn), stars(f.Rating))
			if n := w.FilmNotes[f.Title.Title]; n != "" {
				under += `<div style="margin-top:4px">` + e(n) + `</div>`
			}
			if f.Review != nil {
				under += fmt.Sprintf(`<div style="margin-top:4px;color:%s;font-style:italic">“%s”</div>`, c.soft, e(*f.Review))
			}
			rows.WriteString(s.row(f.Key, f.Poster, f.Title.Title, under))
		}
		parts = append(parts, section("Films", table(rows.String())))
	}
	if len(d.Shows) > 0 && d.Kind != "year" {
		var rows strings.Builder
		for _, sh := range d.Shows {
			span := ""
			if sh.Span != "" {
				span = " · " + e(sh.Span)
			}
			rows.WriteString(s.row(sh.Key, sh.Poster, sh.Title.Title,
				fmt.Sprintf(`<br><span style="color:%s">%s%s</span>`, c.soft, plural(sh.Episodes, "episode"), span)))
		}
		parts = append(parts, section("Shows", table(rows.String())+more(d.More.Shows, "shows")))
	}
	if d.Kind == "month" && (d.Best != nil || len(d.TopGenres) > 0) {
		var bits []string
		if d.Best != nil {
			bits = append(bits, fmt.Sprintf("Film of the month: <b>%s</b> %s", e(d.Best.Title.Title), stars(d.Best.Rating)))
		}
		if len(d.TopGenres) > 0 {
			gs := make([]string, len(d.TopGenres))
			for i, g := range d.TopGenres {
				gs[i] = e(g)
			}
			bits = append(bits, "Most watched: "+strings.Join(gs, ", "))
		}
		parts = append(parts, section("The month", fmt.Sprintf(`<div style="font:14px/1.6 %s;color:%s">%s</div>`, sans, c.ink, strings.Join(bits, "<br>"))))
	}
	if len(d.Picks) > 0 {
		items := make([]gridItem, 0, len(d.Picks))
		for _, p := range d.Picks {
			caption := e(yearText(p.Year))
			if p.InLibrary {
				caption = "In your library"
			}
			items = append(items, gridItem{strPtr(p.Key), p.Title, p.Poster, caption})
		}
		parts = append(parts, section("Up next", fmt.Sprintf(`<div style="font:14px/1.5 %s;color:%s;margin-bottom:8px">%s</div>`, sans, c.ink, e(w.PicksLine))+s.grid(items)))
	}
	if (len(d.Library) > 0 || d.More.Library > 0) && d.Kind != "year" {
		items := make([]gridItem, 0, len(d.Library))
		for _, l := range d.Library {
			items = append(items, gridItem{l.Key, l.Title, l.Poster, e(yearText(l.Year))})
		}
		parts = append(parts, section("New in the library", s.grid(items)+more(d.More.Library, "new in the library")))
	}
	if len(d.Coming) > 0 {
		items := make([]gridItem, 0, len(d.Coming))
		for _, cm := range d.Coming {
			items = append(items, gridItem{cm.Key, cm.Title.Title, cm.Poster, e(cm.Date)})
		}
		parts = append(parts, section("Coming from your watchlist", s.grid(items)+more(d.More.Coming, "coming from your watchlist")))
	}
	off := s.UnsubscribeURL(d.Kind)
	parts = append(parts, fmt.Sprintf(`<tr><td style="padding:26px 28px 28px;font:12px/1.5 %s;color:%s">`+
		`Sent by <a href="%s" style="color:%s">Movie Night</a> to everyone here who has `+
		`it on. <a href="%s" style="color:%s;font-weight:600">Turn this email off</a> `+
		`(or switch any of them in Profile → Emails).</td></tr>`, sans, c.soft, s.cfg.PublicURL, c.soft, e(off), c.accent))
	page = fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">`+
		`<title>%s</title></head><body style="margin:0;padding:0;background:%s">`+
		`<table role="presentation" cellpadding="0" cellspacing="0" width="100%%" style="background:%s"><tr><td align="center" style="padding:24px 12px">`+
		`<table role="presentation" cellpadding="0" cellspacing="0" width="100%%" style="max-width:600px;background:%s;border-radius:14px">`+
		`%s</table></td></tr></table></body></html>`, e(w.Subject), c.bg, c.bg, c.card, strings.Join(parts, ""))

	lines := []string{heading, d.Label + " · " + strings.Join(stat, " · "), "", w.Intro, ""}
	if d.Kind == "year" && d.Year != nil {
		y := d.Year
		if y.Busiest != nil {
			lines = append(lines, "Busiest month: "+*y.Busiest)
		}
		if len(y.Genres) > 0 {
			lines = append(lines, "Favourite genres: "+strings.Join(y.Genres, ", "))
		}
		lines = append(lines, "")
		if len(y.BestFilms) > 0 {
			lines = append(lines, "YOUR BEST FILMS")
			for _, f := range y.BestFilms {
				lines = append(lines, fmt.Sprintf("- %s: %d/10", f.Title.Title, *f.Rating))
			}
			lines = append(lines, "")
		}
		if len(y.TopShows) > 0 {
			lines = append(lines, "MOST WATCHED SHOWS")
			for _, sh := range y.TopShows {
				lines = append(lines, fmt.Sprintf("- %s: %s", sh.Title.Title, plural(sh.Episodes, "episode")))
			}
			lines = append(lines, "")
		}
	}
	if len(d.Films) > 0 && d.Kind != "year" {
		lines = append(lines, "FILMS")
		for _, f := range d.Films {
			l := "- " + f.Title.Title
			if f.Year != nil {
				l += fmt.Sprintf(" (%d)", *f.Year)
			}
			l += ", " + f.WatchedOn
			if f.Rating != nil {
				l += fmt.Sprintf(" · %d/10", *f.Rating)
			}
			lines = append(lines, l)
		}
		lines = append(lines, "")
	}
	if len(d.Shows) > 0 && d.Kind != "year" {
		lines = append(lines, "SHOWS")
		for _, sh := range d.Shows {
			lines = append(lines, strings.TrimSpace(fmt.Sprintf("- %s: %s %s", sh.Title.Title, plural(sh.Episodes, "episode"), sh.Span)))
		}
		if d.More.Shows > 0 {
			lines = append(lines, fmt.Sprintf("...and %d more shows", d.More.Shows))
		}
		lines = append(lines, "")
	}
	if len(d.Picks) > 0 {
		lines = append(lines, "UP NEXT")
		for _, p := range d.Picks {
			lines = append(lines, "- "+p.Title)
		}
	}
	lines = append(lines, "", "Movie Night: "+s.cfg.PublicURL, "Turn this email off: "+off)
	return page, strings.Join(lines, "\n")
}
