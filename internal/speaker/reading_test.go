package speaker

import "testing"

func TestReadingTextHasEveryLanguageAndFallsBack(t *testing.T) {
	for _, l := range ReadingLanguages {
		if len(ReadingText(l)) < 150 {
			t.Errorf("%s text too short to fill ~20 s", l)
		}
	}
	if ReadingText("de") != ReadingText("en") || NormalizeLanguage("") != "en" {
		t.Error("unknown languages should read the English text")
	}
	if ReadingText("tr") == ReadingText("en") {
		t.Error("texts must differ per language")
	}
}
