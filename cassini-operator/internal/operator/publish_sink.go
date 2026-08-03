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
	// AssetDigests maps a site-relative asset path to the sha256 the delivered
	// copy must have. It closes the chain that starts at the seal (D-583): the
	// asset the sink commits is verified to be the artifact the job sealed, not
	// merely a file that arrived under the right name. A sink honours it without
	// knowing anything about sealing, so every destination — including ones that
	// land later — inherits the guarantee. Empty means "nothing to check".
	AssetDigests map[string]string
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
	names := []string{publishSinkLocal, publishSinkNextcloudFiles}
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
// Ordering is the crash-safety argument, and it runs in this direction on
// purpose:
//
//  1. assets   copy → <dir>/.<name>.tmp-<jobID> → rename   (the .opus lands)
//  2. shell    index.html / assets, if the attempt site has them
//  3. manifest cassini.json lineage
//  4. catalog  catalog.json, written atomically, LAST
//
// A crash before 4 leaves an unreferenced file — invisible and harmless. A
// crash after 4 has everything the catalog names already on disk. The reverse
// order would publish a catalog pointing at audio that is not there yet.
//
// Every temp is removed if the delivery fails part-way, because these temps
// live in the *live* site directory now, not a staging tree: a stranded
// `.x.tmp-y` would be served by the file server and swept by nothing.
type localPublishSink struct {
	siteRoot string
	logger   *log.Logger
}

func (s *localPublishSink) Name() string { return publishSinkLocal }

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

	if err := os.MkdirAll(s.siteRoot, 0o755); err != nil {
		return "", fmt.Errorf("create site root: %w", err)
	}

	var pending []string
	defer func() {
		for _, tmp := range pending {
			_ = os.RemoveAll(tmp)
		}
	}()

	for _, entry := range incoming.Meetings {
		assets, err := catalogEntryAssets(entry)
		if err != nil {
			return "", err
		}
		for _, asset := range assets {
			tmp, err := s.stageAsset(d, asset)
			if err != nil {
				return "", err
			}
			if tmp == "" {
				continue
			}
			pending = append(pending, tmp)
		}
	}
	// Commit the staged assets. Rename is atomic within the directory, and the
	// site root and the work root can be different mounts, so the temp has to
	// live beside its destination rather than anywhere in the attempt site.
	for _, tmp := range pending {
		if err := os.Rename(tmp, commitPathForStagedAsset(tmp)); err != nil {
			return "", fmt.Errorf("commit published asset: %w", err)
		}
	}
	pending = nil

	if err := s.refreshSiteShell(d.AttemptSitePath); err != nil {
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
	return s.siteRoot, nil
}

// stagedAssetSuffix marks a half-written asset in the live site. It is chosen
// so the name cannot collide with a published artefact and is obvious in a
// directory listing if one ever survives a crash.
const stagedAssetSuffix = ".cassini-staged"

func commitPathForStagedAsset(tmp string) string {
	return strings.TrimSuffix(tmp, stagedAssetSuffix)
}

// stageAsset copies one catalog-referenced asset next to its destination under
// a temp name, returning that name. It returns "" when the attempt site does
// not carry the asset at all, which is only legitimate for an entry the
// exporter pointed at a remote base URL.
//
// Anything it created is removed when it returns an error: the temp lives in the
// *live* site directory, where a stranded half-written copy would be listed by
// the file server and swept by nothing. The caller's own cleanup only covers
// temps it was told about, which by definition excludes the one that failed.
func (s *localPublishSink) stageAsset(d publishDelivery, asset string) (staged string, err error) {
	source := filepath.Join(d.AttemptSitePath, asset)
	info, err := os.Stat(source)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("attempt site %s is missing catalog asset %s", d.AttemptSitePath, asset)
		}
		return "", fmt.Errorf("stat catalog asset %s: %w", asset, err)
	}

	destination := filepath.Join(s.siteRoot, asset)
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return "", fmt.Errorf("create asset directory: %w", err)
	}
	tmp := destination + stagedAssetSuffix
	if err := os.RemoveAll(tmp); err != nil {
		return "", fmt.Errorf("clear stale staged asset: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(tmp)
		}
	}()
	if info.IsDir() {
		if err := copyDirectory(source, tmp); err != nil {
			return "", err
		}
	} else if err := copyFile(source, tmp, info.Mode()); err != nil {
		return "", err
	}
	// The last link in the seal chain: what is about to be committed must be the
	// artifact the job sealed (D-583). Checked on the staged copy, before the
	// commit, so a mismatch leaves the previously published asset in place.
	//
	// Only file assets are digested. A directory asset is a legacy `artifactPath`
	// export, which the operator's publish path no longer produces and for which
	// there is no single artifact to have sealed.
	if want, ok := d.AssetDigests[filepath.ToSlash(asset)]; ok && !info.IsDir() {
		got, err := fileSHA256(tmp)
		if err != nil {
			return "", err
		}
		if got != want {
			return "", fmt.Errorf("published asset %s does not match the sealed artifact: sha256 %s, want %s", asset, got, want)
		}
	}
	// A rename onto an existing directory fails, so clear the destination for
	// directory assets. Files are replaced by the rename itself.
	if info.IsDir() {
		if err := os.RemoveAll(destination); err != nil {
			return "", fmt.Errorf("clear published asset directory: %w", err)
		}
	}
	return tmp, nil
}

// refreshSiteShell copies the attempt site's top-level files over the live
// site's. The standalone image publishes with --rebuild-viewer so its
// self-contained shell (index.html, assets/) rides along with every publish;
// the wholesale promote used to refresh it implicitly. catalog.json and
// cassini.json are excluded because this sink owns both, and meetings/ because
// the asset pass above owns it.
func (s *localPublishSink) refreshSiteShell(attemptSitePath string) error {
	entries, err := os.ReadDir(attemptSitePath)
	if err != nil {
		return fmt.Errorf("read attempt site: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "catalog.json" || name == "cassini.json" || name == "meetings" {
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
