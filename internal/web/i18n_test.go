package web

import (
	"encoding/json"
	"regexp"
	"sort"
	"testing"
)

// The wizard's text lives in assets/i18n.json rather than in the JavaScript so
// that these two checks are possible at build time. A missing key is a soft
// failure at runtime — the string falls back to English and the page still
// works — which is exactly the kind of fault nobody notices for six months.

type catalogue struct {
	Langs []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Dir  string `json:"dir"`
	} `json:"langs"`
}

func readCatalogue(t *testing.T) (catalogue, map[string]map[string]string) {
	t.Helper()
	raw, err := assets.ReadFile("assets/i18n.json")
	if err != nil {
		t.Fatal(err)
	}
	var meta catalogue
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("the catalogue is not valid JSON: %v", err)
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]string{}
	for _, l := range meta.Langs {
		body, ok := all[l.ID]
		if !ok {
			t.Fatalf("the menu offers %q but the catalogue has no such block", l.ID)
		}
		var dict map[string]string
		if err := json.Unmarshal(body, &dict); err != nil {
			t.Fatalf("%s: %v", l.ID, err)
		}
		out[l.ID] = dict
	}
	return meta, out
}

// Every language answers the same questions, or the ones it does not answer
// silently come out in English.
func TestEveryLanguageHasEveryString(t *testing.T) {
	meta, dicts := readCatalogue(t)
	if len(meta.Langs) < 2 {
		t.Fatal("the catalogue lists fewer than two languages")
	}
	english := dicts["en"]
	if len(english) == 0 {
		t.Fatal("English is the fallback for every other language and it is empty")
	}
	for _, l := range meta.Langs {
		if l.Dir != "ltr" && l.Dir != "rtl" {
			t.Errorf("%s: direction %q is neither ltr nor rtl", l.ID, l.Dir)
		}
		if l.Name == "" {
			t.Errorf("%s has no name for the language menu", l.ID)
		}
		var missing, extra []string
		for k := range english {
			if dicts[l.ID][k] == "" {
				missing = append(missing, k)
			}
		}
		for k := range dicts[l.ID] {
			if _, ok := english[k]; !ok {
				extra = append(extra, k)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)
		if len(missing) > 0 {
			t.Errorf("%s is missing %d string(s): %v", l.ID, len(missing), missing)
		}
		if len(extra) > 0 {
			t.Errorf("%s has %d string(s) English does not: %v", l.ID, len(extra), extra)
		}
	}
}

var i18nKey = regexp.MustCompile(`data-i18n(?:-ph)?="([^"]+)"`)

// Every key the page asks for exists, and every string the catalogue carries is
// asked for by something — either the markup or the JavaScript.
func TestThePageAndTheCatalogueAgree(t *testing.T) {
	_, dicts := readCatalogue(t)
	english := dicts["en"]

	page, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	script, err := assets.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}

	used := map[string]bool{}
	for _, m := range i18nKey.FindAllStringSubmatch(string(page), -1) {
		used[m[1]] = true
		if english[m[1]] == "" {
			t.Errorf("the page asks for %q, which the catalogue does not have", m[1])
		}
	}

	// The rest are looked up from JavaScript, by literal key or, for the
	// per-panel sentences, by an id the Go descriptor supplies.
	body := string(script)
	for k := range english {
		if used[k] {
			continue
		}
		if containsKey(body, k) {
			continue
		}
		t.Errorf("nothing uses the string %q — either the page lost it or it was never wired up", k)
	}
}

// containsKey reports whether the script refers to this catalogue key, either
// as a literal or through the one computed prefix the wizard uses.
func containsKey(script, key string) bool {
	if regexp.MustCompile(`["']` + regexp.QuoteMeta(key) + `["']`).MatchString(script) {
		return true
	}
	// `src.<panel id>` is built at runtime from the descriptor's id.
	return regexp.MustCompile(`^src\.`).MatchString(key) &&
		regexp.MustCompile(`"src\." \+ s\.id`).MatchString(script)
}
