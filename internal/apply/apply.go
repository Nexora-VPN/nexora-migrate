// Package apply writes a chosen subset of a bundle into Nexora.
//
// Three things matter here.
//
// Order: an item is created only after everything it names exists, because a
// template names inbound ids and a client names template ids. The bundle is
// already sorted into that order, and dependencies of a chosen item are pulled
// in automatically — choosing a template without its inbounds would otherwise
// produce a template that routes to nothing.
//
// The fleet: Nexora has no bulk create, and every account created reconciles
// every node it lands on. So the nodes are switched off for the duration and
// synced once each at the end. Without that, a few thousand accounts is an
// hour of pushes and a very unhappy fleet.
//
// Honesty: nothing is retried into a different shape and nothing is silently
// skipped. Every item ends as created, skipped-because-it-exists, or failed
// with the panel's own words.
package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/nexora-vpn/nexora-migrate/internal/bundle"
	"github.com/nexora-vpn/nexora-migrate/internal/target"
)

// Status is how one item ended.
type Status string

const (
	StatusCreated Status = "created"
	StatusExists  Status = "exists"
	StatusFailed  Status = "failed"
	StatusSkipped Status = "skipped"
)

// Event is one step of a run, streamed to the wizard as it happens.
type Event struct {
	Kind    bundle.Kind `json:"kind"`
	ItemID  string      `json:"itemId"`
	Name    string      `json:"name"`
	Status  Status      `json:"status"`
	Message string      `json:"message,omitempty"`
	Done    int         `json:"done"`
	Total   int         `json:"total"`
	// Phase names what the run is doing outside the item list.
	Phase string `json:"phase,omitempty"`
}

// Options tunes one run.
type Options struct {
	// PauseNodes disables every node for the duration and syncs them at the
	// end. On by default, and it is what makes a large import finish.
	PauseNodes bool `json:"pauseNodes"`
}

// Result is the whole run, and is what the final report is built from.
type Result struct {
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	Events   []Event   `json:"events"`
	Created  int       `json:"created"`
	Existing int       `json:"existing"`
	Failed   int       `json:"failed"`
	Skipped  int       `json:"skipped"`
	// Passwords are the generated admin passwords, which exist nowhere else:
	// they are shown once at the end and never written to disk by this tool.
	Passwords map[string]string `json:"passwords,omitempty"`
	Notes     []string          `json:"notes,omitempty"`
}

// Select expands an operator's ticked set into the set that will actually be
// applied: every chosen item plus everything those items depend on. It returns
// the expanded items in apply order and the ids that were added on their behalf.
func Select(b *bundle.Bundle, chosen map[string]bool) (items []bundle.Item, added []string) {
	byID := make(map[string]*bundle.Item, len(b.Items))
	for i := range b.Items {
		byID[b.Items[i].ID] = &b.Items[i]
	}
	want := map[string]bool{}

	var include func(id string, direct bool)
	include = func(id string, direct bool) {
		it, ok := byID[id]
		if !ok || want[id] {
			return
		}
		// A blocked item is never applied, even if something depends on it.
		if it.Severity == bundle.SevBlocked {
			return
		}
		want[id] = true
		// Chosen items are walked in map order, so a dependency may be reached
		// before the operator's own tick of it; only one nobody ticked counts
		// as added on another's behalf.
		if !direct && !chosen[id] {
			added = append(added, id)
		}
		for _, dep := range it.DependsOn {
			include(dep, false)
		}
	}
	for id, on := range chosen {
		if on {
			include(id, true)
		}
	}

	for i := range b.Items {
		if want[b.Items[i].ID] {
			items = append(items, b.Items[i])
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		return bundle.Order(items[i].Kind) < bundle.Order(items[j].Kind)
	})
	return items, added
}

// Run applies the selection. emit is called for every event, including the
// phases either side of the item list; it may be nil.
//
// The result is named so the deferred resume lands in it: the fleet coming
// back — or a node that would not — belongs in the final report.
func Run(ctx context.Context, c *target.Client, items []bundle.Item, opt Options, emit func(Event)) (res Result) {
	res = Result{Started: time.Now(), Passwords: map[string]string{}}
	send := func(e Event) {
		res.Events = append(res.Events, e)
		if emit != nil {
			emit(e)
		}
	}

	paused := pauseNodes(ctx, c, opt, &res, send)
	defer func() {
		resumeNodes(context.WithoutCancel(ctx), c, paused, &res, send)
		res.Finished = time.Now()
	}()

	created := map[string]uint{} // item id → the id Nexora assigned
	total := len(items)

	for i, it := range items {
		if err := ctx.Err(); err != nil {
			res.Skipped += total - i
			send(Event{Phase: "cancelled", Status: StatusSkipped,
				Message: fmt.Sprintf("stopped before %d of %d items", total-i, total),
				Done:    i, Total: total})
			break
		}

		ev := Event{Kind: it.Kind, ItemID: it.ID, Name: it.Name, Done: i + 1, Total: total}

		payload, err := resolve(it, created)
		if err != nil {
			ev.Status, ev.Message = StatusFailed, err.Error()
			res.Failed++
			send(ev)
			continue
		}

		id, err := c.Create(ctx, it.Kind.Path(), payload)
		switch {
		case err == nil:
			created[it.ID] = id
			ev.Status = StatusCreated
			res.Created++
			if pw := adminPassword(it); pw != "" {
				res.Passwords[it.Name] = pw
			}
		case target.IsConflict(err):
			ev.Status = StatusExists
			ev.Message = "the panel already has something with this name, so it was left alone"
			res.Existing++
		case target.IsConfirmRequired(err):
			ev.Status = StatusFailed
			ev.Message = "the panel wants a fresh authenticator code before it creates this: run the import again with this item ticked and a new code, or connect with an API token"
			res.Failed++
		case target.IsLicenceRefusal(err):
			ev.Status, ev.Message = StatusFailed, "the licence is full: "+err.Error()
			res.Failed++
			res.Skipped += total - i - 1
			send(ev)
			send(Event{Phase: "stopped", Status: StatusFailed, Done: i + 1, Total: total,
				Message: "the panel's licence will not accept more of this resource, so the rest of the run was abandoned rather than half-applied"})
			return finish(res)
		default:
			ev.Status, ev.Message = StatusFailed, err.Error()
			res.Failed++
		}
		send(ev)
	}
	return finish(res)
}

func finish(res Result) Result {
	res.Finished = time.Now()
	return res
}

// resolve turns an item's payload into the body to POST, substituting the ids
// Nexora assigned to the things this item names.
func resolve(it bundle.Item, created map[string]uint) (json.RawMessage, error) {
	if len(it.IDRefs) == 0 && len(it.IDRef) == 0 {
		return it.Payload, nil
	}
	var body map[string]any
	if err := json.Unmarshal(it.Payload, &body); err != nil {
		return nil, fmt.Errorf("this item's request body could not be rebuilt: %w", err)
	}
	for field, ids := range it.IDRefs {
		resolved := make([]uint, 0, len(ids))
		for _, dep := range ids {
			if id, ok := created[dep]; ok && id != 0 {
				resolved = append(resolved, id)
			}
		}
		body[field] = resolved
	}
	for field, dep := range it.IDRef {
		if id, ok := created[dep]; ok && id != 0 {
			body[field] = id
		}
	}
	// A client whose templates were not part of this run must not arrive on no
	// template at all — that would leave it provisioned nowhere. Nexora's
	// "everywhere" flag is the right answer, and it is what the panel would
	// have chosen for a create with no template named.
	if emptyList(body["templateIds"]) {
		delete(body, "templateIds")
		body["allTemplates"] = true
	}
	out, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// emptyList reports whether a field holds a list with nothing in it, in either
// of the two shapes it can reach here: the []uint this function just resolved,
// or the []any a payload that was never resolved decoded to.
func emptyList(v any) bool {
	switch list := v.(type) {
	case []uint:
		return len(list) == 0
	case []any:
		return len(list) == 0
	}
	return false
}

// adminPassword digs the generated password back out of an admin's payload so
// the final report can show it. It exists nowhere else.
func adminPassword(it bundle.Item) string {
	if it.Kind != bundle.KindAdmin {
		return ""
	}
	var body struct {
		Password string `json:"password"`
	}
	_ = json.Unmarshal(it.Payload, &body)
	return body.Password
}

// pauseNodes disables every enabled node and returns the ones it touched.
func pauseNodes(ctx context.Context, c *target.Client, opt Options, res *Result, send func(Event)) []target.Node {
	if !opt.PauseNodes {
		res.Notes = append(res.Notes,
			"the nodes were left running during the import, so each account was pushed to the fleet as it was created")
		return nil
	}
	pre, err := c.Check(ctx)
	if err != nil {
		return nil
	}
	var paused []target.Node
	for _, n := range pre.Nodes {
		if !n.Pausable() {
			continue
		}
		if err := c.SetNodeEnabled(ctx, n.ID, false); err != nil {
			res.Notes = append(res.Notes,
				fmt.Sprintf("node %q could not be paused (%v), so it received every account as it was created", n.Name, err))
			continue
		}
		paused = append(paused, n)
	}
	if len(paused) > 0 {
		send(Event{Phase: "nodes paused", Status: StatusCreated,
			Message: fmt.Sprintf("%d node(s) were switched off so the import does not push to the fleet once per account; they are switched back on and synced at the end", len(paused))})
	}
	return paused
}

// resumeNodes switches the fleet back on and pushes once per node. It runs even
// when the import failed or was cancelled: leaving an operator's nodes disabled
// is a far worse outcome than a half-finished import.
func resumeNodes(ctx context.Context, c *target.Client, paused []target.Node, res *Result, send func(Event)) {
	if len(paused) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	for _, n := range paused {
		if err := c.SetNodeEnabled(ctx, n.ID, true); err != nil {
			res.Notes = append(res.Notes,
				fmt.Sprintf("node %q could not be switched back on (%v) — turn it on in the panel", n.Name, err))
			continue
		}
		if err := c.SyncNode(ctx, n.ID); err != nil {
			res.Notes = append(res.Notes,
				fmt.Sprintf("node %q is on again but would not sync (%v) — push it from the panel", n.Name, err))
		}
	}
	send(Event{Phase: "nodes resumed", Status: StatusCreated,
		Message: fmt.Sprintf("%d node(s) were switched back on and synced", len(paused))})
}
