package dns

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Reconciliation: the platform keeps a desired set of records per name it is
// responsible for and makes the zone match it, without churn and without
// duplicates (Phase 28, §3.4).
//
//  1. A record is matched by what it is (type, name, content), not by ID.
//  2. Missing records are created.
//  3. Exact duplicates collapse to one.
//  4. A record the platform created earlier that is no longer wanted is removed.
//  5. A record the platform did not create that clashes is only listed:
//     it is removed only when the person confirms.
//  6. A CNAME is never left beside other records on a name.
//  7. A second run with nothing changed does nothing.

// Want is a record the platform wants to exist.
type Want struct {
	Type     string
	Name     string
	Content  string
	TTL      int   // 0: automatic
	Proxied  *bool // nil: leave as it is
	Priority *int
	// Exclusive: the platform owns the whole (type, name) set, so other
	// contents there are stale or in the way. Additive records (the
	// challenge TXT values) only need to be present: others stay.
	Exclusive bool
	Owner     Owner
}

// Owner says what a record is for.
type Owner struct {
	Kind, ID, Purpose string
}

// Plan actions and reasons.
const (
	ActionCreate = "create"
	ActionKeep   = "keep"
	ActionUpdate = "update"
	ActionDelete = "delete"

	ReasonMissing   = "missing"   // wanted, not there
	ReasonOK        = "ok"        // already right
	ReasonDuplicate = "duplicate" // the same record twice
	ReasonStale     = "stale"     // the platform made it, nobody wants it any more
	ReasonConflict  = "conflict"  // somebody else's record that is in the way
	ReasonSettings  = "settings"  // wanted, but with other TTL/proxy settings
)

// Step is one thing the plan does.
type Step struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
	Record Record `json:"record"`
	Detail string `json:"detail,omitempty"`
	// NeedsConfirm is true for removing a record the platform did not create.
	NeedsConfirm bool  `json:"needsConfirm,omitempty"`
	want         *Want `json:"-"`
}

// PlanResult is what a reconcile would do.
type PlanResult struct {
	Steps []Step `json:"steps"`
}

// Changes counts the steps that change the zone.
func (p PlanResult) Changes() int {
	n := 0
	for _, s := range p.Steps {
		if s.Action != ActionKeep {
			n++
		}
	}
	return n
}

// Conflicts lists the steps that need a person's confirmation.
func (p PlanResult) Conflicts() []Step {
	var out []Step
	for _, s := range p.Steps {
		if s.NeedsConfirm {
			out = append(out, s)
		}
	}
	return out
}

type key struct{ typ, name string }

func normType(t string) string { return strings.ToUpper(strings.TrimSpace(t)) }

// contentKey normalises content for matching: TXT quotes and trailing dots
// of hostnames do not make a record different.
func contentKey(typ, content string) string {
	c := strings.TrimSpace(content)
	switch typ {
	case "TXT":
		c = strings.Trim(c, `"`)
	case "CNAME", "MX", "NS", "SRV":
		c = strings.ToLower(strings.TrimSuffix(c, "."))
	case "AAAA":
		c = strings.ToLower(c)
	}
	return c
}

// Plan works out what to do to make existing match wants. managed is the set
// of record IDs the platform created (and so may remove when stale).
// Only names that appear in wants are touched.
func Plan(existing []Record, wants []Want, managed map[string]bool) PlanResult {
	byKey := map[key][]Record{}
	byName := map[string][]Record{}
	for _, r := range existing {
		r.Type, r.Name = normType(r.Type), Fqdn(r.Name)
		k := key{r.Type, r.Name}
		byKey[k] = append(byKey[k], r)
		byName[r.Name] = append(byName[r.Name], r)
	}
	for _, rs := range byKey {
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].ID < rs[j].ID })
	}

	wantsBy := map[key][]*Want{}
	var order []key
	for i := range wants {
		w := wants[i]
		w.Type, w.Name = normType(w.Type), Fqdn(w.Name)
		k := key{w.Type, w.Name}
		if _, ok := wantsBy[k]; !ok {
			order = append(order, k)
		}
		wantsBy[k] = append(wantsBy[k], &w)
	}

	var steps []Step
	handled := map[string]bool{} // record IDs already given a step
	names := map[string]bool{}
	for _, k := range order {
		names[k.name] = true
	}

	for _, k := range order {
		have := byKey[k]
		exclusive := false
		matched := map[int]bool{}
		for _, w := range wantsBy[k] {
			if w.Exclusive {
				exclusive = true
			}
			found := -1
			for i, r := range have {
				if contentKey(k.typ, r.Content) == contentKey(k.typ, w.Content) && !matched[i] {
					found = i
					break
				}
			}
			if found < 0 {
				steps = append(steps, Step{Action: ActionCreate, Reason: ReasonMissing, Record: w.record(), want: w})
				continue
			}
			matched[found] = true
			r := have[found]
			handled[r.ID] = true
			if (w.Proxied != nil && *w.Proxied != r.Proxied) || (w.TTL != 0 && w.TTL != r.TTL && w.TTL != 1 && r.TTL != 1) {
				nr := r
				if w.Proxied != nil {
					nr.Proxied = *w.Proxied
				}
				if w.TTL != 0 {
					nr.TTL = w.TTL
				}
				steps = append(steps, Step{Action: ActionUpdate, Reason: ReasonSettings, Record: nr, want: w})
			} else {
				steps = append(steps, Step{Action: ActionKeep, Reason: ReasonOK, Record: r})
			}
		}
		// Everything else at (type, name): duplicates of a wanted record, or other contents.
		for i, r := range have {
			if handled[r.ID] {
				continue
			}
			isDup := false
			for j := range have {
				if matched[j] && contentKey(k.typ, have[j].Content) == contentKey(k.typ, r.Content) && j != i {
					isDup = true
					break
				}
			}
			switch {
			case isDup:
				// An exact duplicate: safe to collapse, whoever made it.
				steps = append(steps, Step{Action: ActionDelete, Reason: ReasonDuplicate, Record: r, Detail: "the same record twice"})
				handled[r.ID] = true
			case exclusive && managed[r.ID]:
				steps = append(steps, Step{Action: ActionDelete, Reason: ReasonStale, Record: r, Detail: "no longer needed"})
				handled[r.ID] = true
			case exclusive:
				steps = append(steps, Step{Action: ActionDelete, Reason: ReasonConflict, Record: r, NeedsConfirm: true,
					Detail: "a record you did not create answers this name with other data"})
				handled[r.ID] = true
			}
		}
	}

	// A CNAME and any other record cannot share a name.
	for name := range names {
		rs := byName[name]
		var wantCNAME, wantOther bool
		for _, k := range order {
			if k.name != name {
				continue
			}
			if k.typ == "CNAME" {
				wantCNAME = true
			} else {
				wantOther = true
			}
		}
		for _, r := range rs {
			if handled[r.ID] {
				continue
			}
			clash := (wantCNAME && r.Type != "CNAME") || (wantOther && r.Type == "CNAME")
			if !clash {
				continue
			}
			steps = append(steps, Step{Action: ActionDelete, Reason: ReasonConflict, Record: r, NeedsConfirm: !managed[r.ID],
				Detail: "a CNAME cannot share a name with other records"})
			handled[r.ID] = true
		}
	}

	sort.SliceStable(steps, func(i, j int) bool {
		// Deletes first: a name is free for its new record.
		return rank(steps[i]) < rank(steps[j])
	})
	return PlanResult{Steps: steps}
}

func rank(s Step) int {
	switch s.Action {
	case ActionDelete:
		return 0
	case ActionUpdate:
		return 1
	case ActionCreate:
		return 2
	}
	return 3
}

func (w *Want) record() Record {
	r := Record{Type: w.Type, Name: w.Name, Content: w.Content, TTL: w.TTL, Priority: w.Priority}
	if r.TTL == 0 {
		r.TTL = 1
	}
	if w.Proxied != nil {
		r.Proxied = *w.Proxied
	}
	return r
}

// Applied is what an apply did.
type Applied struct {
	Created, Updated, Deleted int
	Skipped                   []Step // conflicts left because they were not confirmed
}

// Tracker remembers the records the platform created.
type Tracker interface {
	Created(r Record, o Owner) error
	Removed(id string) error
}

// Apply carries a plan out. Steps that need confirmation are skipped unless
// confirmed. A failure stops the run: the zone stays usable and the next
// run finishes the job.
func Apply(ctx context.Context, p Provider, zoneID string, plan PlanResult, confirmed bool, tr Tracker) (Applied, error) {
	var out Applied
	for _, s := range plan.Steps {
		switch s.Action {
		case ActionDelete:
			if s.NeedsConfirm && !confirmed {
				out.Skipped = append(out.Skipped, s)
				continue
			}
			if err := p.Delete(ctx, zoneID, s.Record.ID); err != nil {
				return out, fmt.Errorf("remove %s %s: %w", s.Record.Type, s.Record.Name, err)
			}
			_ = tr.Removed(s.Record.ID)
			out.Deleted++
		case ActionUpdate:
			if _, err := p.Update(ctx, zoneID, s.Record.ID, s.Record); err != nil {
				return out, fmt.Errorf("update %s %s: %w", s.Record.Type, s.Record.Name, err)
			}
			out.Updated++
		case ActionCreate:
			r := s.Record
			if s.want != nil && s.want.Owner.Kind != "" {
				r.Comment = "managed-by=syncloud"
			}
			made, err := p.Create(ctx, zoneID, r)
			if err != nil {
				return out, fmt.Errorf("create %s %s: %w", r.Type, r.Name, err)
			}
			var o Owner
			if s.want != nil {
				o = s.want.Owner
			}
			_ = tr.Created(made, o)
			out.Created++
		}
	}
	return out, nil
}
