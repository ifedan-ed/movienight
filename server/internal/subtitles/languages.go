package subtitles

import "strings"

// Language is one language a person can search in, with the code each
// source uses for it. A source with no code for a language is not asked.
type Language struct {
	Code string // Movie Night's code: ISO 639-1, or pt-br / zh-tw
	Name string // in English, as the player lists it
	// Alpha3 are the ISO 639-2 codes (bibliographic first) Jellyfin and
	// Profile → Players use, so a person's language becomes the default.
	Alpha3 []string

	OpenSubtitles string // "en", "pt-br", "zh-cn"
	SubDL         string // "EN", "BR_PT"
	SubSource     string // "english", "brazilian_portuguese"
	Podnapisi     string // "en", "pt-BR"
	Gestdown      string // Addic7ed's language, as Gestdown reads it ("en")
	// Legacy is the charset an old subtitle in this language most likely
	// uses when it is not UTF-8 (convert.go).
	Legacy string
}

// lang builds the usual case: every source uses the ISO 639-1 code in
// its own spelling. Exceptions are set after.
func lang(code, name, legacy string, alpha3 ...string) Language {
	return Language{Code: code, Name: name, Alpha3: alpha3, OpenSubtitles: code, SubDL: strings.ToUpper(code),
		SubSource: strings.ToLower(name), Podnapisi: code, Gestdown: code, Legacy: legacy}
}

// Languages is every language offered, in the order the player lists them
// (by English name). The source codes come from each site's own list
// (OpenSubtitles' /infos/languages, SubDL's and SubSource's as Bazarr maps
// them, Podnapisi's ISO codes, Addic7ed's through Gestdown).
var Languages = func() []Language {
	l := []Language{
		lang("af", "Afrikaans", "windows-1252", "afr"),
		lang("sq", "Albanian", "windows-1250", "alb", "sqi"),
		lang("ar", "Arabic", "windows-1256", "ara"),
		lang("hy", "Armenian", "", "arm", "hye"),
		lang("eu", "Basque", "windows-1252", "baq", "eus"),
		lang("bn", "Bengali", "", "ben"),
		lang("bs", "Bosnian", "windows-1250", "bos"),
		lang("pt-br", "Brazilian Portuguese", "windows-1252", "pob"),
		lang("bg", "Bulgarian", "windows-1251", "bul"),
		lang("ca", "Catalan", "windows-1252", "cat"),
		lang("zh", "Chinese (simplified)", "gb18030", "chi", "zho"),
		lang("zh-tw", "Chinese (traditional)", "big5"),
		lang("hr", "Croatian", "windows-1250", "hrv"),
		lang("cs", "Czech", "windows-1250", "cze", "ces"),
		lang("da", "Danish", "windows-1252", "dan"),
		lang("nl", "Dutch", "windows-1252", "dut", "nld"),
		lang("en", "English", "windows-1252", "eng"),
		lang("et", "Estonian", "windows-1257", "est"),
		lang("fi", "Finnish", "windows-1252", "fin"),
		lang("fr", "French", "windows-1252", "fre", "fra"),
		lang("gl", "Galician", "windows-1252", "glg"),
		lang("ka", "Georgian", "", "geo", "kat"),
		lang("de", "German", "windows-1252", "ger", "deu"),
		lang("el", "Greek", "windows-1253", "gre", "ell"),
		lang("he", "Hebrew", "windows-1255", "heb"),
		lang("hi", "Hindi", "", "hin"),
		lang("hu", "Hungarian", "windows-1250", "hun"),
		lang("is", "Icelandic", "windows-1252", "ice", "isl"),
		lang("id", "Indonesian", "windows-1252", "ind"),
		lang("it", "Italian", "windows-1252", "ita"),
		lang("ja", "Japanese", "shift_jis", "jpn"),
		lang("ko", "Korean", "euc-kr", "kor"),
		lang("lv", "Latvian", "windows-1257", "lav"),
		lang("lt", "Lithuanian", "windows-1257", "lit"),
		lang("mk", "Macedonian", "windows-1251", "mac", "mkd"),
		lang("ms", "Malay", "windows-1252", "may", "msa"),
		lang("ml", "Malayalam", "", "mal"),
		lang("no", "Norwegian", "windows-1252", "nor", "nob", "nno"),
		lang("fa", "Persian", "windows-1256", "per", "fas"),
		lang("pl", "Polish", "windows-1250", "pol"),
		lang("pt", "Portuguese", "windows-1252", "por"),
		lang("ro", "Romanian", "windows-1250", "rum", "ron"),
		lang("ru", "Russian", "windows-1251", "rus"),
		lang("sr", "Serbian", "windows-1250", "srp"),
		lang("sk", "Slovak", "windows-1250", "slo", "slk"),
		lang("sl", "Slovenian", "windows-1250", "slv"),
		lang("es", "Spanish", "windows-1252", "spa"),
		lang("sw", "Swahili", "windows-1252", "swa"),
		lang("sv", "Swedish", "windows-1252", "swe"),
		lang("tl", "Tagalog", "windows-1252", "tgl", "fil"),
		lang("ta", "Tamil", "", "tam"),
		lang("te", "Telugu", "", "tel"),
		lang("th", "Thai", "windows-874", "tha"),
		lang("tr", "Turkish", "windows-1254", "tur"),
		lang("uk", "Ukrainian", "windows-1251", "ukr"),
		lang("ur", "Urdu", "windows-1256", "urd"),
		lang("vi", "Vietnamese", "windows-1258", "vie"),
	}
	for i := range l {
		x := &l[i]
		switch x.Code {
		case "pt":
			// OpenSubtitles splits Portuguese into Portugal's and Brazil's.
			x.OpenSubtitles = "pt-pt"
		case "pt-br":
			x.SubDL, x.SubSource, x.Podnapisi = "BR_PT", "brazilian_portuguese", "pt-BR"
		case "zh":
			x.OpenSubtitles, x.SubSource = "zh-cn", "chinese_bg_code"
		case "zh-tw":
			// SubDL's ZH_BG is its traditional (Big5) Chinese; SubSource
			// has no separate traditional Chinese.
			x.SubDL, x.SubSource, x.Podnapisi = "ZH_BG", "", "zh-TW"
		case "fa":
			x.SubSource = "farsi_persian"
		}
	}
	return l
}()

// LanguageByCode finds a language by Movie Night's code, case-insensitive.
func LanguageByCode(code string) (Language, bool) {
	code = strings.ToLower(strings.TrimSpace(code))
	for _, l := range Languages {
		if l.Code == code {
			return l, true
		}
	}
	return Language{}, false
}

// DefaultLanguage is the language "Search online" starts in: the person's
// subtitle language from Profile → Players (an ISO 639-2 code, as
// Jellyfin names tracks), else English.
func DefaultLanguage(alpha3 string) Language {
	alpha3 = strings.ToLower(strings.TrimSpace(alpha3))
	if alpha3 != "" {
		for _, l := range Languages {
			for _, a := range l.Alpha3 {
				if a == alpha3 {
					return l
				}
			}
		}
	}
	en, _ := LanguageByCode("en")
	return en
}
