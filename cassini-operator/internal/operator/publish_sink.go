package operator

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A publish sink is where a published meeting goes. The operator selects
// exactly one by name, and that selection is an explicit declaration — never
// inferred from whatever environment happens to be present.
//
// That distinction is the whole point of the seam (D-533). Inferring the
// destination from the AppAPI environment is unsound: a deployment can carry a
// complete-looking APP_ID/APP_SECRET/NEXTCLOUD_URL triple and still be unable
// to use it (harness/bin/ci-e2e-talk-record-roundtrip.sh registers only a
// daemon, so its self-generated secret is unknown to Nextcloud and every
// act-as-user call 401s). A named sink says what the deployment *is* instead of
// guessing from what it looks like.
//
//	                 ┌──────────────────────────────────┐
//	runPublishJob ──▶ │  publishSink (one, by name)      │
//	(no destination   └───────┬──────────────────────────┘
//	 knowledge)               │
//	                    "local" → the site root on this machine
//	                    (Nextcloud Files lands here later, as its own sink)
//
// A sink reports failure by returning an error, and runPublishJob fails the
// publish. There is no best-effort delivery: a meeting that did not reach its
// destination is not a published meeting.
const (
	// publishSinkLocal writes into the operator's own site root.
	publishSinkLocal = "local"
	// defaultPublishSink is what an unset selection resolves to.
	defaultPublishSink = publishSinkLocal
)

// publishDelivery is one meeting's publish output, handed to a sink.
type publishDelivery struct {
	// AttemptSitePath is the site the publish CLI just produced for this
	// attempt. It is the sink's input, never its destination.
	AttemptSitePath string
	JobID           string
	AttemptNumber   int
	// PublishedAtUTC is the publish completion timestamp, recorded as lineage.
	PublishedAtUTC string
}

// publishSink delivers a published meeting to one destination.
type publishSink interface {
	// Name is the selector this sink is chosen by.
	Name() string
	// Deliver places the meeting at the destination and returns the location
	// recorded on the job. An error fails the publish.
	Deliver(ctx context.Context, d publishDelivery) (string, error)
}

// publishSinkNames lists the selectable sinks, for flag help and error text.
func publishSinkNames() []string {
	names := []string{publishSinkLocal}
	sort.Strings(names)
	return names
}

// validatePublishSinkName accepts the empty name (meaning "unset", which
// resolves to the default) and every known sink. A non-empty unrecognised name
// is an error — the operator must never silently publish somewhere the operator
// was not asked to publish.
func validatePublishSinkName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	for _, known := range publishSinkNames() {
		if name == known {
			return nil
		}
	}
	return fmt.Errorf("unknown publish sink %q (known sinks: %s)", name, strings.Join(publishSinkNames(), ", "))
}

// newPublishSink constructs the named sink. It is total over the names
// validatePublishSinkName accepts, so callers that validated first cannot fail
// here.
//
// The empty name deliberately resolves to the default rather than erroring:
// "unset" is not "wrong". Tests construct Config literals directly without
// going through loadConfig, and a nil sink there would panic the whole suite
// rather than exercise the default every deployment actually gets.
func newPublishSink(name string, cfg Config, logger *log.Logger) (publishSink, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = defaultPublishSink
	}
	switch name {
	case publishSinkLocal:
		return &localPublishSink{siteRoot: cfg.SiteRoot, logger: logger}, nil
	default:
		return nil, fmt.Errorf("unknown publish sink %q (known sinks: %s)", name, strings.Join(publishSinkNames(), ", "))
	}
}

// localPublishSink upserts the attempt site's meetings into the operator's live
// site root.
//
// It used to replace the site root wholesale. That is why publishing cost
// O(archive): the exporter re-exported every meeting and the promote rewrote
// every file, so in production 67 meetings took ~7.5 minutes per recording and
// grew from there (D-459). Upserting writes only what actually changed.
//
// Assets are staged before any of them is committed: every one is copied next
// to its destination under a `.cassini-staged` name, and only then renamed into
// place. Nothing already published is touched until the first rename, so a
// failure while staging is a no-op plus a sweep.
//
// The commit order is assets → shell → manifest → catalog. A crash before the
// catalog write leaves an unreferenced file — invisible and harmless — while a
// crash after it has everything the catalog names already on disk. The reverse
// order would publish a catalog pointing at audio that has not landed.
//
// Staged copies live in the *live* site directory, not a staging tree, because
// the work root and the site root are separate mounts in the standalone image
// and a cross-tree rename is EXDEV. A stranded temp there would be served by
// the file server and swept by nothing, so a failed delivery removes every copy
// it staged and reports any it could not.
//
// A delivery is not safe to run concurrently with another: it read-modify-writes
// catalog.json and names its temps after their destination. That is guaranteed
// by there being exactly one publish worker (startPublishWorker), which is a
// correctness requirement of this sink and not just a throughput choice.
type localPublishSink struct {
	siteRoot string
	logger   *log.Logger
}

func (s *localPublishSink) Name() string { return publishSinkLocal }

// Deliver ignores the context deliberately. Cancellation arrives on operator
// shutdown, and by then the choice is between a delivery that finishes in the
// time a few renames take and one abandoned half-way; the staged copies would
// be swept but the meeting would report as failed for no gain. The staging
// phase, which is where the time actually goes, is bounded by one meeting.
func (s *localPublishSink) Deliver(_ context.Context, d publishDelivery) (string, error) {
	incoming, ok, err := loadSiteCatalog(d.AttemptSitePath)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("attempt site %s has no catalog.json", d.AttemptSitePath)
	}
	existing, _, err := loadSiteCatalog(s.siteRoot)
	if err != nil {
		return "", err
	}
	merged, err := upsertSiteCatalog(existing, incoming)
	if err != nil {
		return "", err
	}
	owned, err := ownedAssetRoots(existing, incoming)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(s.siteRoot, 0o755); err != nil {
		return "", fmt.Errorf("create site root: %w", err)
	}

	var staged []stagedAsset
	defer func() {
		s.discardStaged(staged)
	}()

	staged, err = s.stageCatalogAssets(d, incoming)
	if err != nil {
		return "", err
	}

	// Every asset is on disk beside where it belongs; committing is the first
	// destructive act.
	for _, item := range staged {
		if err := s.commitStagedAsset(item); err != nil {
			return "", err
		}
	}
	staged = nil

	if err := s.refreshSiteShell(d.AttemptSitePath, owned); err != nil {
		return "", err
	}
	if err := UpdateLiveSiteManifest(s.siteRoot, SiteBundleLineage{
		JobID:          d.JobID,
		AttemptNumber:  d.AttemptNumber,
		PublishedAtUTC: d.PublishedAtUTC,
	}, len(merged.Meetings)); err != nil {
		return "", err
	}
	if err := writeSiteCatalog(s.siteRoot, merged); err != nil {
		return "", err
	}
	s.sweepOrphanedAssets(existing, incoming)
	return s.siteRoot, nil
}

const (
	// stagedAssetSuffix marks a copy waiting to be committed. It is chosen so
	// the name cannot collide with a published artefact and is obvious in a
	// directory listing if one ever survives a crash.
	stagedAssetSuffix = ".cassini-staged"
	// previousAssetSuffix marks the live copy of a directory asset held aside
	// for the one rename between "the new copy is not in place" and "the old
	// one is no longer needed". Same name #169 gives the same idea for the site
	// shell, so the two collapse into one suffix when they meet.
	previousAssetSuffix = ".cassini-previous"
)

// stagedAsset is one copy waiting beside its destination in the live site.
type stagedAsset struct {
	tmp         string
	destination string
	isDir       bool
}

// stageCatalogAssets stages every file the incoming catalog's entries name.
// The partial list is returned alongside an error so the caller can sweep what
// was staged before the failure.
func (s *localPublishSink) stageCatalogAssets(d publishDelivery, incoming siteCatalog) ([]stagedAsset, error) {
	var staged []stagedAsset
	for _, entry := range incoming.Meetings {
		assets, err := catalogEntryAssets(entry)
		if err != nil {
			return staged, err
		}
		for _, asset := range assets {
			item, err := s.stageAsset(d, asset)
			if err != nil {
				return staged, err
			}
			staged = append(staged, item)
		}
	}
	return staged, nil
}

// ownedAssetRoots is the set of top-level names the asset pass owns, which the
// shell refresh must leave alone.
//
// It is derived from the catalogs rather than hardcoding the exporter's
// directory name. The shell refresh replaces a whole directory, so if the
// exporter ever renamed `meetings/`, a hardcoded skip would quietly hand the
// live archive's audio to a refresh carrying only the meeting being published.
// Both catalogs contribute: a deployment whose incoming entries point at a
// remote base URL (exporter --recordings-base-url, which siteRelativeAsset
// drops) still has local assets named by the live catalog. `meetings` is kept
// as a floor for the case where neither catalog names a local asset and the
// directory nonetheless exists.
func ownedAssetRoots(catalogs ...siteCatalog) (map[string]struct{}, error) {
	owned := map[string]struct{}{"meetings": {}}
	for _, catalog := range catalogs {
		for _, entry := range catalog.Meetings {
			assets, err := catalogEntryAssets(entry)
			if err != nil {
				return nil, err
			}
			for _, asset := range assets {
				root := asset
				if index := strings.Index(asset, string(filepath.Separator)); index > 0 {
					root = asset[:index]
				}
				owned[root] = struct{}{}
			}
		}
	}
	return owned, nil
}

// stageAsset copies one catalog-referenced asset next to its destination in the
// live site, under a temp name. It is pure preparation: nothing already
// published is touched until commitStagedAsset renames the copy into place.
//
// An asset the attempt site does not carry is an error rather than a skip.
// Entries the exporter pointed at a remote base URL never reach here, because
// siteRelativeAsset drops those paths before the sink sees them, so a missing
// source means the publish produced a catalog it cannot back.
func (s *localPublishSink) stageAsset(d publishDelivery, asset string) (stagedAsset, error) {
	source := filepath.Join(d.AttemptSitePath, asset)
	info, err := os.Stat(source)
	if err != nil {
		if os.IsNotExist(err) {
			return stagedAsset{}, fmt.Errorf("attempt site %s is missing catalog asset %s", d.AttemptSitePath, asset)
		}
		return stagedAsset{}, fmt.Errorf("stat catalog asset %s: %w", asset, err)
	}

	destination := filepath.Join(s.siteRoot, asset)
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return stagedAsset{}, fmt.Errorf("create asset directory: %w", err)
	}
	tmp := destination + stagedAssetSuffix
	if err := os.RemoveAll(tmp); err != nil {
		return stagedAsset{}, fmt.Errorf("clear stale staged asset: %w", err)
	}
	if info.IsDir() {
		if err := copyDirectory(source, tmp); err != nil {
			return stagedAsset{}, err
		}
	} else if err := copyFile(source, tmp, info.Mode()); err != nil {
		return stagedAsset{}, err
	}
	return stagedAsset{tmp: tmp, destination: destination, isDir: info.IsDir()}, nil
}

// commitStagedAsset renames one staged copy into place.
//
// A file is replaced by the rename itself, so its destination is never absent.
// A directory cannot be: rename onto an existing directory fails, so the live
// one moves aside first. It is *moved*, not removed — until the staged copy is
// in place the aside is the only complete copy, and a failed rename puts it
// back. A crash in that one-rename window leaves `<name>.cassini-previous` next
// to a missing `<name>`, which the next delivery of that meeting repairs (it
// stages a fresh copy and clears the stale aside). Only directory assets reach
// this path, and the current exporter emits none: portable meetings carry an
// audioPath file.
func (s *localPublishSink) commitStagedAsset(item stagedAsset) error {
	if !item.isDir {
		if err := os.Rename(item.tmp, item.destination); err != nil {
			return fmt.Errorf("commit published asset %s: %w", item.destination, err)
		}
		return nil
	}

	aside := item.destination + previousAssetSuffix
	if err := os.RemoveAll(aside); err != nil {
		return fmt.Errorf("clear stale replaced asset %s: %w", aside, err)
	}
	hasDestination := true
	if _, err := os.Stat(item.destination); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("stat published asset %s: %w", item.destination, err)
		}
		hasDestination = false
	}
	if hasDestination {
		if err := os.Rename(item.destination, aside); err != nil {
			return fmt.Errorf("move previous published asset %s aside: %w", item.destination, err)
		}
	}
	if err := os.Rename(item.tmp, item.destination); err != nil {
		if hasDestination {
			if rollbackErr := os.Rename(aside, item.destination); rollbackErr != nil {
				return fmt.Errorf("commit published asset %s: %w; restore previous: %v", item.destination, err, rollbackErr)
			}
		}
		return fmt.Errorf("commit published asset %s: %w", item.destination, err)
	}
	if hasDestination {
		if err := os.RemoveAll(aside); err != nil {
			s.logf("discard replaced asset %s failed: %v", aside, err)
		}
	}
	return nil
}

// refreshSiteShell copies the attempt site's top-level files over the live
// site's. The standalone image publishes with --rebuild-viewer so its
// self-contained shell (index.html, assets/) rides along with every publish;
// the wholesale promote used to refresh it implicitly. catalog.json and
// cassini.json are excluded because this sink owns both, and the asset roots
// because the pass above owns those.
func (s *localPublishSink) refreshSiteShell(attemptSitePath string, owned map[string]struct{}) error {
	entries, err := os.ReadDir(attemptSitePath)
	if err != nil {
		return fmt.Errorf("read attempt site: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "catalog.json" || name == "cassini.json" {
			continue
		}
		if _, isAssetRoot := owned[name]; isAssetRoot {
			continue
		}
		source := filepath.Join(attemptSitePath, name)
		destination := filepath.Join(s.siteRoot, name)
		if entry.IsDir() {
			if err := os.RemoveAll(destination); err != nil {
				return fmt.Errorf("clear site shell directory %s: %w", name, err)
			}
			if err := copyDirectory(source, destination); err != nil {
				return err
			}
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat site shell entry %s: %w", name, err)
		}
		if err := copyFile(source, destination, info.Mode()); err != nil {
			return err
		}
	}
	return nil
}

// discardStaged removes the copies a failed delivery left beside their
// destinations. A failure to remove one is logged rather than swallowed: these
// live in the served site root, where nothing else sweeps them.
func (s *localPublishSink) discardStaged(staged []stagedAsset) {
	for _, item := range staged {
		if err := os.RemoveAll(item.tmp); err != nil {
			s.logf("discard staged asset %s failed: %v", item.tmp, err)
		}
		if !item.isDir {
			continue
		}
		// An aside is garbage only once its destination is back in place. If a
		// rollback failed, the aside is the last complete copy of a published
		// asset and has to survive for a human to restore.
		aside := item.destination + previousAssetSuffix
		if _, err := os.Stat(item.destination); err != nil {
			if _, asideErr := os.Stat(aside); asideErr == nil {
				s.logf("kept %s: %s is missing and the aside is the only copy left", aside, item.destination)
			}
			continue
		}
		if err := os.RemoveAll(aside); err != nil {
			s.logf("discard replaced asset %s failed: %v", aside, err)
		}
	}
}

// sweepOrphanedAssets removes files the previous version of a republished entry
// named and the new one does not — an entry that moved from an artifactPath
// directory to a portable .opus, say. The wholesale replace used to drop those
// implicitly; an upsert has to be told.
//
// It runs after the catalog write on purpose: by then the files are
// unreferenced, so losing the sweep to a crash leaves invisible garbage rather
// than a broken link. A failure is logged, never returned — the meeting is
// published either way, and failing a delivered publish over leftover bytes
// would be worse than the leftovers.
func (s *localPublishSink) sweepOrphanedAssets(existing, incoming siteCatalog) {
	previous := make(map[string][]string, len(existing.Meetings))
	for _, entry := range existing.Meetings {
		id, err := catalogEntryID(entry)
		if err != nil {
			s.logf("sweep orphaned assets: read existing catalog entry failed: %v", err)
			continue
		}
		assets, err := catalogEntryAssets(entry)
		if err != nil {
			s.logf("sweep orphaned assets: read assets of %s failed: %v", id, err)
			continue
		}
		previous[id] = assets
	}

	for _, entry := range incoming.Meetings {
		id, err := catalogEntryID(entry)
		if err != nil {
			continue
		}
		superseded, ok := previous[id]
		if !ok {
			continue
		}
		current, err := catalogEntryAssets(entry)
		if err != nil {
			continue
		}
		kept := make(map[string]struct{}, len(current))
		for _, asset := range current {
			kept[asset] = struct{}{}
		}
		for _, asset := range superseded {
			if _, stillNamed := kept[asset]; stillNamed {
				continue
			}
			orphan := filepath.Join(s.siteRoot, asset)
			if err := os.RemoveAll(orphan); err != nil {
				s.logf("sweep orphaned asset %s of meeting %s failed: %v", orphan, id, err)
				continue
			}
			s.logf("swept orphaned asset %s of republished meeting %s", orphan, id)
		}
	}
}

// logf reports on the sink's logger, which is nil only for a Runtime built as a
// struct literal (see sink()).
func (s *localPublishSink) logf(format string, args ...any) {
	if s.logger == nil {
		return
	}
	s.logger.Printf("publish sink "+publishSinkLocal+": "+format, args...)
}

// sink returns the runtime's publish sink, defaulting when it is unset.
//
// NewRuntime always populates publishSink, so nil here means a Runtime built
// as a struct literal — which several tests do to exercise the publish worker
// in isolation. Treating that as "unset" rather than panicking keeps the seam
// invisible to code that has no opinion about the destination, and matches the
// rule newPublishSink follows: unset is not wrong, only a non-empty unknown
// name is.
func (rt *Runtime) sink() publishSink {
	if rt.publishSink != nil {
		return rt.publishSink
	}
	return &localPublishSink{siteRoot: rt.cfg.SiteRoot, logger: rt.logger}
}

// publishSinkNameOrDefault renders a possibly-unset selection for logs.
func publishSinkNameOrDefault(name string) string {
	if strings.TrimSpace(name) == "" {
		return defaultPublishSink
	}
	return strings.TrimSpace(name)
}
