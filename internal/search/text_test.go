package search

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

// R33a's text terms (owner, 2026-11-02): quoted phrases and bare words are
// all required; an or: term is an alternative to them together.
func TestParseText(t *testing.T) {
	tests := []struct {
		name, in string
		all, any []string
	}{
		{"nothing", "", nil, nil},
		{"spaces only", "  \t ", nil, nil},
		{"words", "hoses smoke", []string{"hoses", "smoke"}, nil},
		{"two phrases", `"Check hoses" "Smoke"`, []string{"Check hoses", "Smoke"}, nil},
		{"a phrase and words", `"Check hoses" smoke truck`, []string{"Check hoses", "smoke", "truck"}, nil},
		{"a quote left open runs to the end", `smoke "check hoses at`, []string{"smoke", "check hoses at"}, nil},
		{"an empty phrase is no term", `"" smoke`, []string{"smoke"}, nil},
		{"or: a word", "smoke or:fire", []string{"smoke"}, []string{"fire"}},
		{"or: a phrase", `"Smoke showing" or:"grass fire"`, []string{"Smoke showing"}, []string{"grass fire"}},
		{"several or:", `"Smoke showing" or:fire or:alarm`, []string{"Smoke showing"}, []string{"fire", "alarm"}},
		{"or: alone is no term", "smoke or: fire", []string{"smoke", "fire"}, nil},
		{"or: at the end is no term", "smoke or:", []string{"smoke"}, nil},
		{"OR: in capitals is the key too", "smoke OR:fire", []string{"smoke"}, []string{"fire"}},
		{`"or:" in quotes is text`, `"or:"`, []string{"or:"}, nil},
		{`"or:fire" in quotes is text`, `"or:fire" smoke`, []string{"or:fire", "smoke"}, nil},
		{"colons in text are text", "ADDR: 14:05", []string{"ADDR:", "14:05"}, nil},
		{"a word with or: inside is text", "door:open color:red", []string{"door:open", "color:red"}, nil},
		{"only or: terms", "or:fire or:smoke", nil, []string{"fire", "smoke"}},
	}
	for _, tt := range tests {
		got := ParseText(tt.in)
		if !reflect.DeepEqual(got, Terms{All: tt.all, Any: tt.any}) {
			t.Errorf("%s: ParseText(%q) = all %q, any %q; want all %q, any %q", tt.name, tt.in, got.All, got.Any, tt.all, tt.any)
		}
	}
}

// Each term is escaped for ILIKE on its own and wrapped in %…%, so a % or _
// in what's typed matches itself.
func TestTextTermsAsPatterns(t *testing.T) {
	p, err := Filter{Text: `"100% done" a_b or:"x\y"`}.Params(nil, nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{`%100\% done%`, `%a\_b%`}; !reflect.DeepEqual(p.QAll, want) {
		t.Errorf("QAll %q, want %q", p.QAll, want)
	}
	if want := []string{`%x\\y%`}; !reflect.DeepEqual(p.QAny, want) {
		t.Errorf("QAny %q, want %q", p.QAny, want)
	}
	if p, _ := (Filter{}).Params(nil, nil, 50); p.QAll == nil || p.QAny == nil || len(p.QAll)+len(p.QAny) != 0 {
		t.Errorf("no text: QAll %q, QAny %q, want two empty arrays", p.QAll, p.QAny)
	}
}

func TestTooManyTerms(t *testing.T) {
	loc := time.UTC
	ok := strings.Repeat("w ", MaxTerms)
	if _, err := Parse(url.Values{"q": {ok}}, false, loc); err != nil {
		t.Errorf("%d terms: %v", MaxTerms, err)
	}
	if _, err := Parse(url.Values{"q": {ok + " or:one"}}, false, loc); err == nil {
		t.Errorf("%d terms accepted", MaxTerms+1)
	}
}
