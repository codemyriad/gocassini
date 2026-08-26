package operator

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// One-shot migration of a legacy in-container archive into Nextcloud Files
// (D-613).
//
// This exists because of what it replaces. Delivery to Nextcloud Files became a
// mandatory, per-meeting publish stage (D-549/D-533), and the ExApp stopped
// keeping a durable published archive of its own (D-550) — but an install that
// predates all of that still has one sitting on its persistent volume, and
// nothing publishes those meetings again. Convergence used to happen by itself,
// on every AppAPI enabled edge, by re-uploading the whole archive; that was
// unbounded work on a fixed deadline and it failed in the worst way available
// (D-540/D-541). So convergence is now something a human does, once, knowingly.
//
//	$SITE_ROOT/                      Nextcloud Files (owner: cassini)
//	  catalog.json      ─┐             Cassini/Recordings/
//	  meetings/                          ├─ meetings/<id>.opus   ← objects FIRST
//	    <id>.opus       ─┴─ backfill ──▶ └─ catalog.json         ← index LAST
//
// It is NOT a reconciliation loop and must not grow into one. It refuses to run
// against a destination that already holds recordings, which is what keeps it a
// migration: the only archive it is ever correct to write into is an empty one.
// Reconciling a populated archive needs a source of truth this command does not
// have (which sealed attempt is current, what has already converged) and is
// tracked separately as D-586.
const backfillNCFilesCommand = "backfill-nc-files"

// ncBackfillTimeout bounds the whole run. Generous rather than tight: unlike
// the sync it replaces this is interactive, a human is watching it, and there
// is no next attempt that silently papers over a timeout.
const ncBackfillTimeout = 6 * time.Hour

// Exit codes. The caller acts on these, so they distinguish the three things an
// admin would do differently — not merely success from failure.
//
// The distinction that matters is whether anything was WRITTEN. "Retry is safe"
// and "retry will make it worse" are opposite instructions, and every failure
// before the upload loop belongs to the first group: telling someone to go and
// delete half-uploaded recordings when nothing was uploaded points them at a
// live archive.
const (
	// backfillExitNothingToDo: the destination is already populated, or there is
	// no legacy archive to migrate. Nothing was written; nothing is wrong.
	backfillExitNothingToDo = 3
	// backfillExitNotStarted: the run failed before writing anything. Fix the
	// cause and re-run — no cleanup.
	backfillExitNotStarted = 4
	// backfillExitPartial (the generic failure code): writing had begun, so the
	// destination may hold objects the catalog does not name.
	backfillExitPartial = 1
)

// errBackfillRefused marks an answer of "there is nothing to migrate here" —
// either the destination is already populated or the source is empty. Both are
// legitimate outcomes of asking the question, not failures, and both leave
// everything untouched.
var errBackfillRefused = errors.New("refused")

// backfillNCFiles is the delivery, minus the transport. Split out so the tests
// can drive it against an httptest server without a process boundary.
type backfillNCFiles struct {
	cfg    ExAppConfig
	client *http.Client
	// public grants the virtual all-users group read on every backfilled
	// recording. See the flag's help text for why this is a real decision.
	public bool
	out    io.Writer
	// wrote records that at least one mutating request was sent, so a failure
	// can say whether the destination might now be half-written.
	wrote bool
}

// runBackfillNCFiles is the `cassini-operator backfill-nc-files` entry point.
func runBackfillNCFiles(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cassini-operator "+backfillNCFilesCommand, flag.ContinueOnError)
	fs.SetOutput(stderr)

	repoRoot, err := findRepoRoot()
	if err != nil {
		repoRoot = ""
	}
	siteRoot := fs.String("site-root", defaultSiteRoot(persistentStorageRoot(), defaultOperatorDataRoot(repoRoot)),
		"published site to read the legacy archive from")
	public := fs.Bool("public", false,
		"grant every signed-in account read on the backfilled recordings, instead of leaving them\nowner-only. Decide before the run: the migration cannot be re-run to widen them afterwards")
	dryRun := fs.Bool("dry-run", false,
		"check the guard and report what would be uploaded, without writing anything")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Backfill a legacy in-container published archive into Nextcloud Files.

Run this ONCE, by hand, after updating an installation that published
recordings before they were stored in Nextcloud Files. Normal publishing needs
nothing from this command.

It refuses to run when Nextcloud Files already holds recordings, because the
only archive it is correct to write into is an empty one.

Usage:
  cassini-operator `+backfillNCFilesCommand+` [--dry-run] [--public] [--site-root DIR]

Flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected arguments: %v\n", fs.Args())
		return 2
	}

	exapp, err := LoadExAppConfig()
	if err != nil {
		// Not backfillExitPartial: this fails before the guard has even run, so
		// nothing was written and the runner must not tell an admin to go and
		// delete recordings — which, on a healthy install, are their live ones.
		fmt.Fprintf(stderr, "exapp config: %v\n", err)
		return backfillExitNotStarted
	}
	if !exapp.appAPIActive() {
		// Almost always "you ran this on the host instead of inside the app
		// container": the act-as-user credential only exists in there.
		fmt.Fprint(stderr, "NEXTCLOUD_URL, APP_ID and APP_SECRET are not all set, so there is no Nextcloud to back up to.\n"+
			"Run this inside the Cassini app container, where AppAPI injects them.\n")
		return 2
	}

	ctx, cancel := context.WithTimeout(ctx, ncBackfillTimeout)
	defer cancel()

	b := &backfillNCFiles{
		cfg:    exapp,
		client: &http.Client{Timeout: ncFilesUploadTimeout},
		public: *public,
		out:    stdout,
	}
	if err := b.run(ctx, *siteRoot, *dryRun); err != nil {
		fmt.Fprintf(stderr, "backfill: %v\n", err)
		switch {
		case errors.Is(err, errBackfillRefused):
			return backfillExitNothingToDo
		case !b.wrote:
			return backfillExitNotStarted
		default:
			return backfillExitPartial
		}
	}
	return 0
}

// run performs the migration: read the local archive, refuse unless the
// destination is empty, then upload objects before the index.
func (b *backfillNCFiles) run(ctx context.Context, siteRoot string, dryRun bool) error {
	local, err := loadBackfillSource(siteRoot)
	if err != nil {
		return err
	}
	b.printf("source %s: %d meeting(s), %d recording file(s)", siteRoot, len(local.catalog.Meetings), len(local.uploads))

	if err := b.guardDestinationIsEmpty(ctx); err != nil {
		return err
	}

	if dryRun {
		b.printf("dry run: destination is empty; would upload %d file(s) into %s and write catalog.json", len(local.uploads), ncRecordingsRoot)
		for _, u := range local.uploads {
			b.printf("  would upload %s", u.remote)
		}
		return nil
	}

	// Past this point the destination may have been modified, so a failure can
	// no longer promise that re-running is safe.
	b.wrote = true
	for _, dir := range []string{
		path.Dir(ncRecordingsRoot),     // Cassini
		ncRecordingsRoot,               // Cassini/Recordings
		ncRecordingsRoot + "/meetings", // Cassini/Recordings/meetings
	} {
		if dir == "." || dir == "" {
			continue
		}
		if err := b.cfg.davMkcol(ctx, b.client, ncRecordingsOwner, dir); err != nil {
			return fmt.Errorf("mkcol %s: %w", dir, err)
		}
	}

	// Objects first, index last — the same ordering the publish sink uses and
	// for the same reason: a run that dies half-way leaves files nothing points
	// at, never an index pointing at files that are not there.
	for i, u := range local.uploads {
		// Rules before bytes, exactly as the publish sink does it (D-594). A
		// leaf inherits the container's read grant to the virtual all-users
		// group, so uploading first would make every recording in the archive
		// readable by every account for the length of its own upload — minutes
		// per file here, and this command exists to move many at once. Reserving
		// the leaf empty and denying it first costs one request per file and
		// leaves nothing exposed but a zero-byte name.
		//
		// The deny survives the upload that follows because an overwriting PUT
		// preserves the fileid, which is what groupfolders keys ACL rows by.
		if _, err := b.cfg.davPutEmpty(ctx, b.client, ncRecordingsOwner, u.remote, "audio/ogg"); err != nil {
			return fmt.Errorf("reserve %s (%d of %d): %w", u.remote, i+1, len(local.uploads), err)
		}
		if err := b.cfg.davProppatchACLRules(ctx, b.client, ncRecordingsOwner, u.remote, recordingACLRules(nil, b.public)); err != nil {
			return fmt.Errorf("protect %s: %w", u.remote, err)
		}
		status, err := b.cfg.davPutFileStatus(ctx, b.client, ncRecordingsOwner, u.remote, u.local, "audio/ogg")
		if err != nil {
			return fmt.Errorf("upload %s (%d of %d): %w", u.remote, i+1, len(local.uploads), err)
		}
		b.printf("uploaded %s (%d/%d, %s)", u.remote, i+1, len(local.uploads), backfillStatusWord(status))
	}

	if err := b.writeCatalog(ctx, local.catalog); err != nil {
		return err
	}
	b.printf("done: %d recording(s) in %s", len(local.uploads), ncRecordingsRoot)
	if !b.public {
		// Deliberately does NOT offer a re-run with --public: the destination is
		// now populated, so the guard would refuse. Widening after the fact is
		// Nextcloud's job, and this is a one-shot migration by design.
		b.printf("These recordings are readable only by %q. Grant access from the Files app "+
			"(Advanced permissions on Cassini/Recordings/meetings/). This migration cannot be "+
			"re-run to widen them.", ncRecordingsOwner)
	}
	return nil
}

// writeCatalog publishes the index last, reserved and denied before it holds
// anything, then locked to the owner again once it does.
//
// The local catalog is written as-is rather than merged, which is safe only
// because the guard proved the destination had no catalog worth merging. The
// publish sink read-merge-writes for exactly the opposite situation.
func (b *backfillNCFiles) writeCatalog(ctx context.Context, catalog siteCatalog) error {
	if catalog.Meetings == nil {
		catalog.Meetings = []json.RawMessage{}
	}
	body, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal catalog: %w", err)
	}
	body = append(body, '\n')
	remote := ncRecordingsRoot + "/catalog.json"
	// Rules before bytes, for the same reason as the recordings above (D-594),
	// and on a leaf the guard has just proven is absent: PUTting the body
	// outright creates the authoritative unfiltered index of every migrated
	// meeting — ids, titles, room names, dates — with no rules of its own,
	// inheriting the container's `everyone: READ` until the PROPPATCH lands.
	//
	// Unconditional rather than gated on "was it missing", because
	// guardDestinationIsEmpty admits only an absent or meeting-less catalog:
	// there is never a body here worth preserving. That also makes the aborted
	// state safe — if the deny below fails, the run stops having created a
	// zero-byte file rather than a readable index of the whole archive, which
	// matters because selfHealLeafProtection only ever visits `.opus` leaves.
	if _, err := b.cfg.davPutEmpty(ctx, b.client, ncRecordingsOwner, remote, "application/json"); err != nil {
		return fmt.Errorf("reserve catalog: %w", err)
	}
	if err := b.cfg.davProppatchACLRules(ctx, b.client, ncRecordingsOwner, remote, catalogProtectionACLRules()); err != nil {
		return fmt.Errorf("protect catalog before filling it: %w", err)
	}
	if err := b.cfg.davPutBytes(ctx, b.client, ncRecordingsOwner, remote, body, "application/json"); err != nil {
		return fmt.Errorf("put catalog: %w", err)
	}
	// The unfiltered index stays private to the owner: the operator reads it as
	// the owner and serves each caller a filtered view.
	if err := b.cfg.davProppatchACLRules(ctx, b.client, ncRecordingsOwner, remote, catalogProtectionACLRules()); err != nil {
		return fmt.Errorf("protect catalog: %w", err)
	}
	b.printf("wrote %s", remote)
	return nil
}

// guardDestinationIsEmpty is the whole reason this command is safe to hand to
// an admin. It answers one question — "has this install already moved past the
// migration point?" — and refuses if the answer is anything but a clear no.
//
// Two independent checks, because neither alone is sound:
//
//   - catalog.json, which is what the operator asked for. But it is written
//     LAST, so a delivery that died half-way leaves recordings behind with no
//     catalog naming them.
//   - the meetings/ collection, which catches exactly that case.
//
// Every ambiguous answer refuses. In particular the status is what is branched
// on, never the error: davGetBytes returns a nil error for a 403 or a 500, so
// "err == nil" does not mean "the file is not there".
func (b *backfillNCFiles) guardDestinationIsEmpty(ctx context.Context) error {
	catalogRemote := ncRecordingsRoot + "/catalog.json"
	raw, status, err := b.cfg.davGetBytes(ctx, b.client, ncRecordingsOwner, catalogRemote)
	switch {
	case err != nil && status == 0:
		// Transport failure: we learned nothing about the destination.
		return fmt.Errorf("read %s: %w", catalogRemote, err)
	case err != nil:
		return fmt.Errorf("read %s (HTTP %d): %w", catalogRemote, status, err)
	case status == http.StatusNotFound:
		// No index yet. Fall through to the recordings check.
	case status >= 200 && status < 300:
		var existing siteCatalog
		if jsonErr := json.Unmarshal(raw, &existing); jsonErr != nil {
			return fmt.Errorf("%w: %s exists in Nextcloud Files but is not readable as a catalog (%v). "+
				"Refusing to overwrite it — inspect it by hand", errBackfillRefused, catalogRemote, jsonErr)
		}
		if len(existing.Meetings) > 0 {
			return fmt.Errorf("%w: %s already lists %d meeting(s). This installation is past the migration point; "+
				"a backfill would overwrite the live archive index. Nothing was changed",
				errBackfillRefused, catalogRemote, len(existing.Meetings))
		}
	default:
		return fmt.Errorf("%w: reading %s returned HTTP %d, so whether the archive is empty is unknown. Nothing was changed",
			errBackfillRefused, catalogRemote, status)
	}

	meetingsRemote := ncRecordingsRoot + "/meetings"
	names, visible, err := b.cfg.davPropfindNames(ctx, b.client, ncRecordingsOwner, meetingsRemote)
	if err != nil {
		return fmt.Errorf("list %s: %w", meetingsRemote, err)
	}
	if visible && len(names) > 0 {
		return fmt.Errorf("%w: %s already holds %d recording(s) even though the catalog is empty — a delivery may have "+
			"been interrupted. Nothing was changed; resolve this by hand", errBackfillRefused, meetingsRemote, len(names))
	}
	return nil
}

func (b *backfillNCFiles) printf(format string, args ...any) {
	if b.out == nil {
		return
	}
	fmt.Fprintf(b.out, format+"\n", args...)
}

// backfillUpload is one local file and where it goes.
type backfillUpload struct{ local, remote string }

// backfillSource is the legacy archive as read from disk.
type backfillSource struct {
	catalog siteCatalog
	uploads []backfillUpload
}

// loadBackfillSource reads the local archive and resolves every asset its
// catalog names, refusing before any upload if one is missing.
//
// The catalog is the manifest, not the meetings/ directory listing: a file on
// disk that no catalog entry points at is not a published meeting, and copying
// it across would put a recording into Nextcloud that no index describes and
// nothing can attribute an audience to.
func loadBackfillSource(siteRoot string) (backfillSource, error) {
	siteRoot = strings.TrimSpace(siteRoot)
	if siteRoot == "" {
		return backfillSource{}, errors.New("--site-root must not be empty")
	}
	catalog, ok, err := loadSiteCatalog(siteRoot)
	if err != nil {
		return backfillSource{}, err
	}
	// An absent or empty local archive is the ordinary state of any installation
	// created after recordings moved to Nextcloud Files — the site root is never
	// written under that sink. So it is a "nothing to migrate" answer, in the
	// same class as a destination that is already populated, and NOT a failure:
	// reporting it as one would tell an admin their migration broke when in fact
	// there was never anything to migrate.
	if !ok {
		return backfillSource{}, fmt.Errorf("%w: %s has no catalog.json, so there is no legacy archive here to migrate. "+
			"An installation that only ever published into Nextcloud Files has nothing to do here", errBackfillRefused, siteRoot)
	}
	if len(catalog.Meetings) == 0 {
		return backfillSource{}, fmt.Errorf("%w: %s/catalog.json lists no meetings, so there is nothing to migrate",
			errBackfillRefused, siteRoot)
	}

	var uploads []backfillUpload
	seen := map[string]bool{}
	for _, entry := range catalog.Meetings {
		assets, err := catalogEntryAssets(entry)
		if err != nil {
			return backfillSource{}, err
		}
		id, err := catalogEntryID(entry)
		if err != nil {
			return backfillSource{}, err
		}
		if len(assets) == 0 {
			return backfillSource{}, fmt.Errorf("catalog entry %q names no local asset (a remote recordings base URL cannot be backed up)", id)
		}
		for _, asset := range assets {
			local := filepath.Join(siteRoot, asset)
			info, err := os.Stat(local)
			if err != nil {
				return backfillSource{}, fmt.Errorf("catalog entry %q names %s, which is not on disk: %w", id, asset, err)
			}
			if info.IsDir() {
				// Pre-portable exports carried a directory per meeting. Those
				// predate the .opus archive this destination stores and cannot
				// be delivered as a single object; say so instead of skipping.
				return backfillSource{}, fmt.Errorf("catalog entry %q points at directory %s, not a portable .opus. "+
					"This archive predates portable meetings and cannot be backfilled", id, asset)
			}
			remote := ncRecordingsRoot + "/" + filepath.ToSlash(asset)
			if seen[remote] {
				continue
			}
			seen[remote] = true
			uploads = append(uploads, backfillUpload{local: local, remote: remote})
		}
	}
	// Deterministic order so a re-run after a partial failure reports the same
	// sequence, and a human can tell how far the previous run got.
	sort.Slice(uploads, func(i, j int) bool { return uploads[i].remote < uploads[j].remote })
	return backfillSource{catalog: catalog, uploads: uploads}, nil
}

func backfillStatusWord(status int) string {
	if status == http.StatusCreated {
		return "created"
	}
	return fmt.Sprintf("HTTP %d", status)
}
