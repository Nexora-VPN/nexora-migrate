// Package bundle holds the canonical shape everything in this tool speaks.
//
// A source reader turns whatever the old panel stores into a []Item; the web
// wizard renders those items as a tree and lets the operator tick a subset; the
// applier walks the ticked subset in dependency order and POSTs each item's
// payload to Nexora. Nothing else knows what a Marzban row or an x-ui inbound
// looks like.
//
// The one rule that keeps this honest: an Item's Payload is *the request body
// we intend to send*, built at read time and reviewable before anything is
// written. It is not a private data model that gets translated later.
package bundle

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Kind is what an item becomes in Nexora. The order of these constants is the
// order items must be applied in: a template names inbound ids, a client names
// template ids, so the things being named have to exist first.
type Kind string

const (
	KindRuleSet  Kind = "ruleset"
	KindInbound  Kind = "inbound"
	KindOutbound Kind = "outbound"
	KindEndpoint Kind = "endpoint"
	KindTemplate Kind = "template"
	KindAdmin    Kind = "admin"
	KindClient   Kind = "client"
)

// applyOrder is the dependency order. Anything not listed sorts last.
var applyOrder = map[Kind]int{
	KindRuleSet: 0, KindInbound: 1, KindOutbound: 2, KindEndpoint: 3,
	KindTemplate: 4, KindAdmin: 5, KindClient: 6,
}

// Order reports the apply rank of a kind.
func Order(k Kind) int {
	if n, ok := applyOrder[k]; ok {
		return n
	}
	return 99
}

// Path is the Nexora API path a kind is created at.
func (k Kind) Path() string {
	switch k {
	case KindRuleSet:
		return "/api/rule-sets"
	case KindInbound:
		return "/api/inbounds"
	case KindOutbound:
		return "/api/outbounds"
	case KindEndpoint:
		return "/api/endpoints"
	case KindTemplate:
		return "/api/templates"
	case KindAdmin:
		return "/api/admins"
	case KindClient:
		return "/api/users"
	}
	return ""
}

// Label is the human name of a kind, used for the tree's top level.
func (k Kind) Label() string {
	switch k {
	case KindRuleSet:
		return "Rule sets"
	case KindInbound:
		return "Inbounds"
	case KindOutbound:
		return "Outbounds"
	case KindEndpoint:
		return "Endpoints"
	case KindTemplate:
		return "Templates (routing / DNS)"
	case KindAdmin:
		return "Admins"
	case KindClient:
		return "Clients"
	}
	return string(k)
}

// Severity marks how much of an item survived the conversion.
type Severity string

const (
	SevOK      Severity = "ok"      // converts cleanly
	SevWarn    Severity = "warn"    // converts, but something was changed or dropped
	SevBlocked Severity = "blocked" // cannot be transferred at all
)

// Item is one thing that can be moved. Everything the wizard shows and
// everything the applier sends is in here.
type Item struct {
	// ID is stable within a bundle and is how dependencies are named:
	// "inbound:vless-443", "template:default", "client:ali_1401".
	ID   string `json:"id"`
	Kind Kind   `json:"kind"`

	// Name is what the item will be called in Nexora (already normalised).
	Name string `json:"name"`
	// SourceName is what it was called in the old panel. Equal to Name when
	// nothing had to be changed.
	SourceName string `json:"sourceName"`
	// Group is the tree path under the kind, e.g. {"vless"} or {"reseller2"}.
	// Empty puts the item directly under its kind.
	Group []string `json:"group,omitempty"`

	// Severity and Notes are the review surface. A blocked item is shown,
	// explained, and cannot be ticked.
	Severity Severity `json:"severity"`
	Notes    []string `json:"notes,omitempty"`

	// Detail is the small table of facts the wizard shows next to the name
	// (protocol, port, quota, expiry…). Display only.
	Detail map[string]string `json:"detail,omitempty"`

	// Payload is the JSON body POSTed to Kind.Path(), minus the id fields that
	// only exist after their dependencies are created.
	Payload json.RawMessage `json:"payload"`

	// IDRefs late-binds a list of ids: field name in Payload → item IDs whose
	// created Nexora ids go there as a []uint. Resolved by the applier.
	IDRefs map[string][]string `json:"idRefs,omitempty"`
	// IDRef is the same for a single id (a user's owning admin). A reference
	// whose target was not selected is dropped, not failed: an account whose
	// reseller was left behind still belongs to the panel.
	IDRef map[string]string `json:"idRef,omitempty"`

	// DependsOn is every item that must be created first. IDRefs targets are
	// added to it automatically by Validate.
	DependsOn []string `json:"dependsOn,omitempty"`
}

// AddNote records a conversion note and raises severity to warn if the item was
// otherwise clean. Blocked items keep their severity.
func (i *Item) AddNote(format string, args ...any) {
	i.Notes = append(i.Notes, fmt.Sprintf(format, args...))
	if i.Severity == "" || i.Severity == SevOK {
		i.Severity = SevWarn
	}
}

// Block marks the item as impossible to transfer and says why.
func (i *Item) Block(format string, args ...any) {
	i.Notes = append(i.Notes, fmt.Sprintf(format, args...))
	i.Severity = SevBlocked
}

// Set writes a display fact.
func (i *Item) Set(key, value string) {
	if value == "" {
		return
	}
	if i.Detail == nil {
		i.Detail = map[string]string{}
	}
	i.Detail[key] = value
}

// SourceInfo describes where a bundle came from. It is shown on every wizard
// step so an operator never loses track of which panel they are reading.
type SourceInfo struct {
	Panel   string `json:"panel"`   // "3x-ui", "s-ui", "marzban", …
	Version string `json:"version"` // detected, best effort
	Origin  string `json:"origin"`  // file path or base URL
}

// Bundle is one extraction.
type Bundle struct {
	Source SourceInfo `json:"source"`
	Items  []Item     `json:"items"`
	// Notes are bundle-wide remarks — things that are true of the whole
	// migration rather than of one item.
	Notes []string `json:"notes,omitempty"`
}

// Add appends an item.
func (b *Bundle) Add(it Item) {
	if it.Severity == "" {
		it.Severity = SevOK
	}
	b.Items = append(b.Items, it)
}

// Note records a bundle-wide remark, ignoring duplicates.
func (b *Bundle) Note(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	for _, n := range b.Notes {
		if n == msg {
			return
		}
	}
	b.Notes = append(b.Notes, msg)
}

// Validate folds IDRefs into DependsOn, drops references to items that were
// never produced, and sorts the bundle into apply order. It is called once by
// every reader before the bundle leaves the package.
func (b *Bundle) Validate() {
	known := make(map[string]bool, len(b.Items))
	for _, it := range b.Items {
		known[it.ID] = true
	}
	for idx := range b.Items {
		it := &b.Items[idx]
		seen := map[string]bool{}
		var deps []string
		add := func(id string) {
			if id == "" || seen[id] || !known[id] {
				return
			}
			seen[id] = true
			deps = append(deps, id)
		}
		for _, id := range it.DependsOn {
			add(id)
		}
		for field, id := range it.IDRef {
			if !known[id] {
				delete(it.IDRef, field)
				continue
			}
			add(id)
		}
		for field, ids := range it.IDRefs {
			kept := ids[:0]
			for _, id := range ids {
				if known[id] {
					kept = append(kept, id)
					add(id)
				}
			}
			it.IDRefs[field] = kept
		}
		it.DependsOn = deps
	}
	sort.SliceStable(b.Items, func(x, y int) bool {
		a, c := b.Items[x], b.Items[y]
		if Order(a.Kind) != Order(c.Kind) {
			return Order(a.Kind) < Order(c.Kind)
		}
		return a.Name < c.Name
	})
}

// Counts returns how many items of each kind the bundle holds.
func (b *Bundle) Counts() map[Kind]int {
	out := map[Kind]int{}
	for _, it := range b.Items {
		out[it.Kind]++
	}
	return out
}

// Node is one row of the selection tree the wizard renders. A node is either a
// group (Children non-empty) or a leaf carrying an item.
type Node struct {
	Key      string  `json:"key"`
	Label    string  `json:"label"`
	Kind     Kind    `json:"kind"`
	Item     *Item   `json:"item,omitempty"`
	Children []*Node `json:"children,omitempty"`
	// Count and Blocked summarise the subtree so a group row can say
	// "412 items, 3 blocked" without the client walking it.
	Count   int `json:"count"`
	Blocked int `json:"blocked"`
}

// Tree groups the bundle into kind → group path → item, which is exactly the
// nested selection the wizard needs: tick a kind to take everything of it, tick
// one group to take that group, or tick single rows.
func (b *Bundle) Tree() []*Node {
	var roots []*Node
	byKind := map[Kind]*Node{}

	for idx := range b.Items {
		it := &b.Items[idx]
		root, ok := byKind[it.Kind]
		if !ok {
			root = &Node{Key: "kind:" + string(it.Kind), Label: it.Kind.Label(), Kind: it.Kind}
			byKind[it.Kind] = root
			roots = append(roots, root)
		}
		parent := root
		path := root.Key
		for _, seg := range it.Group {
			path += "/" + seg
			child := findChild(parent, path)
			if child == nil {
				child = &Node{Key: path, Label: seg, Kind: it.Kind}
				parent.Children = append(parent.Children, child)
			}
			parent = child
		}
		parent.Children = append(parent.Children, &Node{
			Key: it.ID, Label: it.Name, Kind: it.Kind, Item: it, Count: 1,
			Blocked: boolToInt(it.Severity == SevBlocked),
		})
	}

	sort.SliceStable(roots, func(i, j int) bool {
		return Order(roots[i].Kind) < Order(roots[j].Kind)
	})
	for _, r := range roots {
		summarise(r)
	}
	return roots
}

func findChild(parent *Node, key string) *Node {
	for _, c := range parent.Children {
		if c.Key == key {
			return c
		}
	}
	return nil
}

func summarise(n *Node) {
	if n.Item != nil {
		return
	}
	sort.SliceStable(n.Children, func(i, j int) bool {
		a, b := n.Children[i], n.Children[j]
		if (a.Item == nil) != (b.Item == nil) {
			return a.Item == nil // groups before leaves
		}
		return a.Label < b.Label
	})
	n.Count, n.Blocked = 0, 0
	for _, c := range n.Children {
		summarise(c)
		n.Count += c.Count
		n.Blocked += c.Blocked
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
