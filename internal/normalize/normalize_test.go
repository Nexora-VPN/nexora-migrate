package normalize

import "testing"

func TestKeepsPersianAndOtherLetters(t *testing.T) {
	n := NewNamer()
	res := n.Name("علی‌رضا", "fallback")
	if res.Name == "" || res.Name == "fallback" {
		t.Fatalf("a Persian name must survive; got %q", res.Name)
	}
	// The zero-width non-joiner is invisible, so two names that look identical
	// would differ. It goes; the letters stay.
	if res.Name != "علیرضا" {
		t.Errorf("name = %q, want the invisible joiner removed and nothing else", res.Name)
	}
	if !res.Changed || res.Note == "" {
		t.Error("a changed name must be reported")
	}
}

func TestUnchangedNameIsSilent(t *testing.T) {
	res := NewNamer().Name("ali_1401", "x")
	if res.Changed || res.Note != "" {
		t.Errorf("a clean name must pass through untouched: %+v", res)
	}
}

func TestSpacesAndUnsafeCharacters(t *testing.T) {
	res := NewNamer().Name("reza kh?/2", "x")
	if res.Name != "reza_kh_2" {
		t.Errorf("name = %q, want spaces and URL-breaking characters as underscores", res.Name)
	}
}

// Nexora's name column is unique, so two rows that clean to the same thing must
// not collide — the second would be refused and the operator would lose an
// account without being told which.
func TestCollisionsGetASuffix(t *testing.T) {
	n := NewNamer()
	a := n.Name("ali kh", "x")
	b := n.Name("ali/kh", "y")
	if a.Name == b.Name {
		t.Fatalf("both cleaned to %q", a.Name)
	}
	if b.Name != a.Name+"_2" {
		t.Errorf("second name = %q, want %q", b.Name, a.Name+"_2")
	}
	if b.Note == "" {
		t.Error("a collision rename must say what it collided with")
	}
}

// Seeding with what the target panel already holds stops an import silently
// colliding with an account that is already there.
func TestReservedNamesAreAvoided(t *testing.T) {
	n := NewNamer()
	n.Reserve("ali")
	res := n.Name("ali", "x")
	if res.Name == "ali" {
		t.Error("a reserved name must not be handed out again")
	}
}

func TestEmptyNameFallsBack(t *testing.T) {
	res := NewNamer().Name("???", "client-41")
	if res.Name != "client-41" {
		t.Errorf("name = %q, want the fallback", res.Name)
	}
	if res.Note == "" {
		t.Error("falling back to a generated name must be reported")
	}
}

func TestLongNamesAreTrimmed(t *testing.T) {
	long := ""
	for i := 0; i < 200; i++ {
		long += "a"
	}
	res := NewNamer().Name(long, "x")
	if len([]rune(res.Name)) != maxLen {
		t.Errorf("length = %d, want %d", len([]rune(res.Name)), maxLen)
	}
}
