// Package normalize turns a source panel's names into names Nexora accepts,
// and says out loud whenever it had to change one.
//
// The rules are deliberately conservative. A name is a user's identity in the
// panel, it is what an operator searches for, and for s-ui it is also the
// subscription id — so the only changes made are the ones that would otherwise
// break something: characters that cannot survive a URL path or a config file,
// and collisions, which Nexora's unique index would reject outright.
//
// Persian and other non-ASCII letters are kept. Nexora puts no charset
// constraint on a user name, 3x-ui client "emails" are routinely Persian, and
// transliterating them would leave an operator unable to find their own
// customers.
package normalize

import (
	"fmt"
	"strings"
	"unicode"
)

// Namer normalises names within one namespace and remembers what it has already
// handed out, so two source rows that collapse to the same name get different
// Nexora names instead of one overwriting the other.
type Namer struct {
	taken map[string]string // normalised (lowered) → the source name that claimed it
}

// NewNamer returns an empty namespace.
func NewNamer() *Namer { return &Namer{taken: map[string]string{}} }

// Reserve marks a name as already in use — used to seed the namespace with what
// the target panel already holds, so an import never silently collides with an
// account that is already there.
func (n *Namer) Reserve(name string) {
	if name == "" {
		return
	}
	n.taken[strings.ToLower(name)] = name
}

// Result is one normalisation.
type Result struct {
	Name    string // the name to use in Nexora
	Source  string // the name in the old panel
	Changed bool
	Note    string // why it changed; empty when it did not
}

// Name normalises one name. fallback is used when nothing usable is left (an
// empty or entirely-invalid source name), and should be something stable and
// unique such as "client-41".
func (n *Namer) Name(source, fallback string) Result {
	cleaned, reasons := clean(source)
	if cleaned == "" {
		cleaned = fallback
		reasons = append(reasons, "the source name was empty or had no usable characters")
	}
	if len(cleaned) > maxLen {
		cleaned = trimTo(cleaned, maxLen)
		reasons = append(reasons, fmt.Sprintf("shortened to %d characters", maxLen))
	}

	final := cleaned
	if prev, clash := n.taken[strings.ToLower(final)]; clash {
		for i := 2; ; i++ {
			candidate := fmt.Sprintf("%s_%d", trimTo(cleaned, maxLen-4), i)
			if _, used := n.taken[strings.ToLower(candidate)]; !used {
				final = candidate
				break
			}
		}
		reasons = append(reasons, fmt.Sprintf("the name %q was already taken (by %q)", cleaned, prev))
	}
	n.taken[strings.ToLower(final)] = source

	res := Result{Name: final, Source: source, Changed: final != source}
	if res.Changed {
		if len(reasons) == 0 {
			reasons = append(reasons, "adjusted to a form the panel accepts")
		}
		res.Note = fmt.Sprintf("renamed %q → %q: %s", source, final, strings.Join(reasons, "; "))
	}
	return res
}

// maxLen is a self-imposed ceiling. Nexora does not enforce one, but a name is
// shown in tables, written into generated link remarks and searched on, and a
// 400-character client "email" pasted out of a spreadsheet helps nobody.
const maxLen = 64

// clean strips what cannot survive, and reports what it did.
func clean(s string) (string, []string) {
	var reasons []string
	var b strings.Builder
	var (
		droppedControl bool
		replacedSpace  bool
		droppedUnsafe  bool
	)
	for _, r := range s {
		switch {
		case r == '\uFEFF' || unicode.Is(unicode.Cf, r) || unicode.IsControl(r):
			// Zero-width joiners and RTL marks are invisible, so two names that
			// look identical would differ, and a control character in a config
			// file is a parse error waiting to happen.
			droppedControl = true
		case unicode.IsSpace(r):
			b.WriteByte('_')
			replacedSpace = true
		case strings.ContainsRune(unsafe, r):
			// Replaced rather than dropped: dropping merges names that were
			// distinct ("a/b" and "ab" both become "ab"), and a silent merge
			// costs an operator an account.
			b.WriteByte('_')
			droppedUnsafe = true
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Trim(collapse(b.String()), "_-.")
	if droppedControl {
		reasons = append(reasons, "removed invisible characters")
	}
	if replacedSpace {
		reasons = append(reasons, "spaces became underscores")
	}
	if droppedUnsafe {
		reasons = append(reasons, "characters a URL or config cannot carry became underscores")
	}
	return out, reasons
}

// unsafe is the set that breaks a subscription path, a shell quote, a JSON
// string read by hand, or a sing-box tag.
const unsafe = `/\?#%&+"'` + "`" + `<>{}[]|^~:;,*$!()=`

func collapse(s string) string {
	var b strings.Builder
	var last rune
	for _, r := range s {
		if r == '_' && last == '_' {
			continue
		}
		b.WriteRune(r)
		last = r
	}
	return b.String()
}

func trimTo(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.Trim(string(r[:n]), "_-.")
}

// Tag normalises a sing-box tag: same idea, but tags also may not contain
// spaces at all and are compared byte-for-byte by the node, so they stay
// conservative.
func (n *Namer) Tag(source, fallback string) Result {
	res := n.Name(source, fallback)
	if strings.ContainsAny(res.Name, " \t") {
		res.Name = strings.ReplaceAll(res.Name, " ", "_")
		res.Changed = true
	}
	return res
}
