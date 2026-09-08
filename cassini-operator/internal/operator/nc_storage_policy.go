package operator

import (
	"fmt"
	"sort"
	"strings"
)

// What a mode switch does with the recordings that are already there (D-708).
//
// Until now there was one answer, hard-coded: copy everything the destination
// does not already have, skip by name what it does, then empty the source. That
// is a reasonable default and it is the wrong thing to assume, because the three
// situations it collapses are genuinely different decisions:
//
//	an archive at the source only        copying it is obviously right
//	archives at BOTH roots               whose is authoritative?
//	the SAME recording under both        which copy survives?
//
// So the policy is two fields, and the second one only means anything when the
// first is `merge`:
//
//	                    ┌── switch_only ── copy nothing. The recordings stay
//	                    │                   where they are and the mode moves
//	                    │                   without them.
//	  Strategy ─────────┼── merge ───────── carry the source across, and on a
//	                    │        │          name that exists at both:
//	                    │        ├── newest_wins  keep whichever was written
//	                    │        │                last. Compared, not assumed.
//	                    │        └── skip         keep the destination's, and
//	                    │                         keep the source's IN the source
//	                    └── overwrite ───── clear the destination first, then
//	                                        carry everything across.
//
// The default is `{merge, skip}`, which is exactly what the first pass did — so
// an unconflicted switch behaves as it always has, and the controls only appear
// when the answer would differ.

const (
	// storageStrategySwitchOnly moves the mode and nothing else.
	//
	// It is the only strategy that leaves the source populated on purpose. The
	// recordings become a stranded archive — reported on /storage and in the
	// Setup tab, readable by nobody through Cassini until the mode moves back —
	// which is a legitimate thing to want when the two roots hold different
	// halves of an instance's history and somebody is still deciding.
	storageStrategySwitchOnly = "switch_only"
	// storageStrategyMerge carries the source across and keeps whatever the
	// destination already had that the source does not.
	storageStrategyMerge = "merge"
	// storageStrategyOverwrite clears the destination and then carries the
	// source across, so the destination ends up being exactly the source.
	//
	// It is the one strategy that DELETES recordings the switch was not asked to
	// move: names at the destination the source does not have. That is what it
	// is for, and it is why the preview counts them separately and says so.
	storageStrategyOverwrite = "overwrite"

	// storageConflictNewestWins keeps whichever copy was written last.
	//
	// "Last write wins" is compared, not assumed. The destination is not the
	// later write by definition — an archive of immutable `<id>.opus`
	// publications can perfectly well have the source's copy be the newer one,
	// and a policy that could not say so would be indistinguishable from skip.
	storageConflictNewestWins = "newest_wins"
	// storageConflictSkip keeps the destination's copy AND leaves the source's
	// where it is, so both survive. It is the only thing that makes a switch
	// leave anything behind in the source once the mode has moved.
	storageConflictSkip = "skip"
)

// storageMigrationPolicy is the pair, as it crosses the wire and as the engine
// reads it.
type storageMigrationPolicy struct {
	Strategy   string `json:"strategy,omitempty"`
	OnConflict string `json:"on_conflict,omitempty"`
}

// defaultStorageMigrationPolicy is what a request that names no policy gets. It
// is the first pass's behaviour exactly, so a switch with nothing to decide is
// unchanged by all of this.
func defaultStorageMigrationPolicy() storageMigrationPolicy {
	return storageMigrationPolicy{Strategy: storageStrategyMerge, OnConflict: storageConflictSkip}
}

// normalizeStorageMigrationPolicy fills in what was not asked for and refuses
// what cannot be honoured.
//
// An unrecognised value is an error rather than a silent fallback. The whole
// point of the field is that the caller has an opinion about what happens to an
// archive; quietly substituting a different opinion is how a `switch_only`
// typo empties a source.
func normalizeStorageMigrationPolicy(in storageMigrationPolicy) (storageMigrationPolicy, error) {
	out := defaultStorageMigrationPolicy()
	if strategy := strings.TrimSpace(in.Strategy); strategy != "" {
		switch strategy {
		case storageStrategySwitchOnly, storageStrategyMerge, storageStrategyOverwrite:
			out.Strategy = strategy
		default:
			return out, fmt.Errorf("unknown strategy %q; expected %q, %q or %q",
				in.Strategy, storageStrategySwitchOnly, storageStrategyMerge, storageStrategyOverwrite)
		}
	}
	if onConflict := strings.TrimSpace(in.OnConflict); onConflict != "" {
		switch onConflict {
		case storageConflictNewestWins, storageConflictSkip:
			out.OnConflict = onConflict
		default:
			return out, fmt.Errorf("unknown on_conflict %q; expected %q or %q",
				in.OnConflict, storageConflictNewestWins, storageConflictSkip)
		}
	}
	// `on_conflict` outside a merge is not an error; it is simply not consulted.
	// Refusing it would make the UI's job harder for no gain — a form that
	// remembers the conflict choice while the strategy is switched away from
	// merge and back is the ordinary shape.
	return out, nil
}

// Asked reports whether the caller expressed an opinion at all. A request that
// did not is refused when there IS a choice to make — see errStorageChoiceRequired.
func (p storageMigrationPolicy) Asked() bool {
	return strings.TrimSpace(p.Strategy) != ""
}

// copiesAnything reports whether this policy moves recordings at all.
func (p storageMigrationPolicy) copiesAnything() bool {
	return p.Strategy != storageStrategySwitchOnly
}

// clearsSource reports whether the source is emptied once the mode has moved.
//
// Every strategy but `switch_only` does, minus whatever a skipped conflict kept
// there — the source is cleared so an instance does not carry two copies of its
// archive forever, and `switch_only` exists precisely to not do that.
func (p storageMigrationPolicy) clearsSource() bool {
	return p.Strategy != storageStrategySwitchOnly
}

// clearsDestinationFirst is overwrite, and only overwrite.
func (p storageMigrationPolicy) clearsDestinationFirst() bool {
	return p.Strategy == storageStrategyOverwrite
}

// storageMigrationChoice is what the two roots make necessary, computed from one
// probe and rendered by the UI (D-708).
//
// The rule the spec states is "do not show the controls when there is no choice
// to make", and these are the two facts that decide it:
//
//	StrategyMatters    recordings in BOTH roots. Switch-only, merge and
//	                   overwrite then produce three different archives.
//	ConflictMatters    the SAME recording in both. Only then does HOW to merge
//	                   mean anything.
//
// Both are false when either root could not be read, and that is deliberate: an
// unanswered question is not "no conflict". The switch itself re-asks under its
// own lock, and refuses a policy-free request that finds one.
type storageMigrationChoice struct {
	Comparable      bool
	StrategyMatters bool
	ConflictMatters bool
	Conflicts       []string
}

// storageChoiceFor works out what a switch from `source` to `destination` has to
// ask about. Roots are passed as facts rather than as modes so the same
// computation serves the preview, the switch and the wizard.
func storageChoiceFor(source, destination ncArchiveFacts) storageMigrationChoice {
	out := storageMigrationChoice{Comparable: source.Probed && destination.Probed}
	if !out.Comparable {
		return out
	}
	out.StrategyMatters = source.Populated() && destination.Populated()
	out.Conflicts = duplicateNames(source, destination)
	out.ConflictMatters = len(out.Conflicts) > 0
	return out
}

// Required reports whether the administrator has to be asked before the switch
// may run without a policy.
func (c storageMigrationChoice) Required() bool {
	return c.StrategyMatters || c.ConflictMatters
}

// archiveCarryPlan is what one policy decides for one pair of trees, worked out
// before anything is written.
//
// Deciding first and acting second is what makes the plan reportable: the
// preview runs exactly this function and renders the counts, so the numbers an
// administrator confirms are produced by the code that will act on them rather
// than by a second implementation that can drift from it.
type archiveCarryPlan struct {
	// Copy is a name the destination does not have.
	Copy []string
	// Replace is a name it does have, that this policy takes from the source
	// anyway. Replacing is delete-then-copy, never `Overwrite: T`.
	Replace []string
	// Skip is a name the destination keeps.
	Skip []string
	// KeepInSource is the subset of Skip whose source copy also survives — the
	// `skip` conflict policy, and nothing else. It is what the tidy-up excludes
	// and what the recovery must not delete.
	KeepInSource []string
	// DeleteAtDestination is what `overwrite` removes because the source does
	// not have it. Nothing else ever produces a non-empty value here.
	DeleteAtDestination []string
}

// planArchiveCarry decides, per name, what this policy does.
func planArchiveCarry(policy storageMigrationPolicy, source, destination ncArchiveFacts) archiveCarryPlan {
	var plan archiveCarryPlan
	if !policy.copiesAnything() {
		// switch_only: everything stays exactly where it is, on both sides.
		plan.KeepInSource = source.Names()
		return plan
	}

	present := destination.byName()
	if policy.clearsDestinationFirst() {
		sourceNames := source.byName()
		for _, entry := range destination.Entries {
			if _, ok := sourceNames[entry.Name]; !ok {
				plan.DeleteAtDestination = append(plan.DeleteAtDestination, entry.Name)
			}
		}
		sort.Strings(plan.DeleteAtDestination)
	}

	for _, entry := range source.Entries {
		existing, clash := present[entry.Name]
		switch {
		case !clash:
			plan.Copy = append(plan.Copy, entry.Name)
		case policy.clearsDestinationFirst():
			// Overwrite has no conflicts by construction: the destination's copy
			// is being removed either way, so every source name is carried.
			plan.Replace = append(plan.Replace, entry.Name)
		case policy.OnConflict == storageConflictNewestWins && sourceIsNewer(entry, existing):
			plan.Replace = append(plan.Replace, entry.Name)
		case policy.OnConflict == storageConflictSkip:
			plan.Skip = append(plan.Skip, entry.Name)
			plan.KeepInSource = append(plan.KeepInSource, entry.Name)
		default:
			// newest_wins where the destination is newer, or where neither side
			// carries a usable timestamp. The destination's copy stays and the
			// source's does NOT survive the tidy-up: last-write-wins converges on
			// one copy, which is the difference between it and skip.
			plan.Skip = append(plan.Skip, entry.Name)
		}
	}
	sort.Strings(plan.Copy)
	sort.Strings(plan.Replace)
	sort.Strings(plan.Skip)
	sort.Strings(plan.KeepInSource)
	return plan
}

// sourceIsNewer answers the `newest_wins` comparison.
//
// A missing timestamp on EITHER side means "cannot say", and cannot-say never
// wins: the destination keeps its copy. Treating an unreadable date as very old
// would let a comparison replace a recording on the strength of a value nobody
// managed to read, and the direction of that mistake is a deletion.
func sourceIsNewer(source, destination davEntry) bool {
	if source.Modified.IsZero() || destination.Modified.IsZero() {
		return false
	}
	return source.Modified.After(destination.Modified)
}

// carriedNames is every name this plan puts at the destination, which is also
// the set of catalog entries the SOURCE's copy should win.
func (p archiveCarryPlan) carriedNames() []string {
	out := make([]string, 0, len(p.Copy)+len(p.Replace))
	out = append(out, p.Copy...)
	out = append(out, p.Replace...)
	sort.Strings(out)
	return out
}

// keepSet is KeepInSource as a lookup, for the tidy-up and the recovery.
func (p archiveCarryPlan) keepSet() map[string]bool {
	if len(p.KeepInSource) == 0 {
		return nil
	}
	out := make(map[string]bool, len(p.KeepInSource))
	for _, name := range p.KeepInSource {
		out[name] = true
	}
	return out
}

// nameSet is the generic form, for a list that arrived from the settings file.
func nameSet(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[name] = true
	}
	return out
}

// sortedNames turns a lookup back into a stable list, for writing down.
func sortedNames(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// catalogIDFor maps an archive entry's file name to the meeting id its catalog
// entry is keyed by.
//
// `<id>.opus` is what every publish writes, and a legacy directory-shaped export
// is named by the bare id — so trimming the extension covers both. A name that
// is neither still produces a stable key, which is what matters: the id is only
// ever used to decide which side of a merge an entry comes from.
func catalogIDFor(name string) string {
	return strings.TrimSuffix(name, ".opus")
}
