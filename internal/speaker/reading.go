package speaker

// readingTexts are what a person reads aloud when recording their voice:
// ordinary sentences covering many sounds, a little over 20 s at a natural pace.
var readingTexts = map[string]string{
	"tr": "Bugün hava çok güzel, sabah erkenden kalkıp kahvemi içtim ve uzun bir yürüyüşe çıktım. " +
		"Yolda eski dostlarımla karşılaştık, birlikte çay içip geçen yılın anılarını konuştuk. " +
		"Akşama doğru eve döndüm, biraz kitap okudum ve yarınki toplantı için notlarımı hazırladım.",
	"en": "The weather is lovely today, so I got up early, had my coffee and went out for a long walk. " +
		"On the way I ran into some old friends and we talked about last year over a cup of tea. " +
		"Towards evening I came home, read for a while and prepared my notes for tomorrow's meeting.",
}

// ReadingLanguages lists the languages that have a reading text, in menu order.
var ReadingLanguages = []string{"tr", "en"}

// LanguageName is the language's own name, for menus.
func LanguageName(lang string) string {
	if lang == "tr" {
		return "Türkçe"
	}
	return "English"
}

// NormalizeLanguage returns lang when it has a reading text, else "en".
func NormalizeLanguage(lang string) string {
	if _, ok := readingTexts[lang]; ok {
		return lang
	}
	return "en"
}

// ReadingText is the text to read aloud in lang (English for other languages).
func ReadingText(lang string) string { return readingTexts[NormalizeLanguage(lang)] }
