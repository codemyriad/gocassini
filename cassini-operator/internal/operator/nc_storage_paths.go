package operator

import "strings"

// Where each storage model keeps its archive (D-616 followups).
//
// Until this file existed there was one constant — `Cassini/Recordings` — and
// both models addressed it. That was D-660's recorded verdict ("the collision is
// benign, so a mode-branched path constant buys nothing"), and it was right about
// writes and wrong about everything that has to reason about WHERE the archive
// is.
//
// What the shared name costs, measured against the first pass:
//
//	a Team folder mounted at `Cassini` WINS that path and Nextcloud physically
//	renames the service account's own `Cassini` directory to `Cassini (N)` — a
//	server-chosen suffix (D-660 bench). So the two models can never be at their
//	path at the same time, which makes every operation that spans them a
//	recovery exercise: the archive has to be DISCOVERED by a regex, moving
//	between models has to be a MOVE rather than a copy (the source path is about
//	to become the destination path), a staging directory has to exist to hold
//	the archive while the mount comes down, and bringing the mount down needs a
//	Group Folders write that Nextcloud refuses to an ExApp.
//
// Two roots that cannot shadow each other remove all of it:
//
//	┌───────────────────────────────────────────────────────────────────────┐
//	│  the `cassini` service account's Files                                │
//	│                                                                       │
//	│   CassiniNoACL/Recordings   its OWN directory. Nothing can be mounted  │
//	│                             over it, no other account has any mount of │
//	│                             it. The DEFAULT model lives here.          │
//	│                                                                       │
//	│   Cassini/Recordings        a Team folder mounted at `Cassini`, with   │
//	│                             advanced ACLs. The ACCESS-CONTROLLED model │
//	│                             lives here.                                │
//	└───────────────────────────────────────────────────────────────────────┘
//
// Both keep the same internal shape (`meetings/<id>.opus` beside
// `catalog.json`), so everything downstream of the root string is unchanged.
const (
	// ncRecordingsMount is the Team folder's mount point. It used to be derived
	// from the recordings root with firstPathSegment(); it is its own constant
	// now, because the root it was derived from is the DEFAULT model's root and
	// no group folder is ever mounted there.
	ncRecordingsMount = "Cassini"

	// ncACLRecordingsRoot is the access-controlled archive: inside the Team
	// folder, under per-recording ACLs. Unchanged from the first pass, and
	// unchanged on every install that already has one — the access-controlled
	// model needs no migration.
	ncACLRecordingsRoot = ncRecordingsMount + "/Recordings"

	// ncDefaultRecordingsMount is the first segment of the default model's root.
	// Named separately because two checks care about it: nothing may be mounted
	// there (which is what makes the tree private), and the collision suffix
	// pattern must NOT match it.
	ncDefaultRecordingsMount = "CassiniNoACL"

	// ncDefaultRecordingsRoot is the deps-free archive: the service account's own
	// directory, readable by nobody but it, served to every caller by the
	// operator acting as the owner.
	ncDefaultRecordingsRoot = ncDefaultRecordingsMount + "/Recordings"

	// ncLegacyDefaultRecordingsRoot is where the default model's archive lived
	// before the split — the same path the Team folder wants. Installs from the
	// first pass have their recordings here (or under a server-renamed
	// `Cassini (N)`), and the enabled-edge adoption carries them across. Nothing
	// writes here any more.
	ncLegacyDefaultRecordingsRoot = ncACLRecordingsRoot
)

// recordingsRootFor names the archive root of one storage model.
//
// Every caller that already knows which model it is acting for uses this
// directly, rather than asking the process-wide record again: the publish sink
// reads the mode ONCE per delivery so a mode that changes mid-delivery cannot
// leave one asset in each tree, and the same argument applies to every other
// multi-step operation.
func recordingsRootFor(accessControlled bool) string {
	if accessControlled {
		return ncACLRecordingsRoot
	}
	return ncDefaultRecordingsRoot
}

// ncArchiveRoot is the root for callers that have no mode in hand.
//
// It resolves through ncStorage.accessControlled(), which answers `true` when
// the mode is not resolved yet — so an unresolved process addresses the Team
// folder. That is the same direction every other unresolved branch takes and it
// is the one that fails closed: reading the access-controlled root as a caller
// who has no mount of it returns 404, where reading the private root as the
// owner would serve the archive on the strength of a mode nobody has decided.
func ncArchiveRoot() string {
	return recordingsRootFor(ncStorage.accessControlled())
}

// firstPathSegment returns the first path component of a slash path, e.g.
// "Cassini/Recordings" -> "Cassini".
func firstPathSegment(p string) string {
	p = strings.Trim(p, "/")
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return p
}

// recordingsTreeDirs lists a recordings root's collections, outermost first, so
// MKCOL can walk them in creatable order. Derived from the root rather than
// hard-coded, because the same shape is built under three different roots: the
// two models', and whichever legacy tree the adoption finds.
func recordingsTreeDirs(root string) []string {
	parts := strings.Split(strings.Trim(root, "/"), "/")
	dirs := make([]string, 0, len(parts)+1)
	for i := range parts {
		if parts[i] == "" {
			continue
		}
		dirs = append(dirs, strings.Join(parts[:i+1], "/"))
	}
	return append(dirs, strings.Trim(root, "/")+"/meetings")
}
