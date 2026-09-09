package cassini

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// `cassini dev meetings pull` mirrors a Nextcloud meeting archive into a local
// directory shaped like a published site, so a development stack can be seeded
// with real recordings instead of a synthetic fixture.
//
//	<out>/
//	  catalog.json          the catalog envelope + the server's entries, verbatim
//	  meetings/<id>.opus    one per entry, exactly as published
//	  seed-manifest.json    provenance: where from, when, what, how big
//
// That layout is not invented here. It is what the viewer's exporter writes,
// what `cassini-operator backfill-nc-files` reads, and what the published
// catalog's own `audioPath` values already claim ("./meetings/<id>.opus"). A
// pack is therefore a drop-in site root as well as a harness seed.
//
// WHY IT LIVES UNDER `dev` and not beside `meetings`. The `meetings` commands
// are the surface an agent is pointed at: find a meeting, read it, keep a copy.
// Bulk-mirroring an archive onto a laptop to feed a test stack is a development
// act, so it sits with `dev stack` and `dev room`. It is in this package, not a
// new one, so the reviewed client — credential resolution, redirect refusal,
// the room/date filter, the published-tree check on every asset path — is
// reused rather than re-implemented.
//
// READ-ONLY, LIKE EVERYTHING ELSE HERE. Every request is a GET or a HEAD. There
// is no flag that writes anything back to the Nextcloud it read from.
//
// PRIVACY. A pack is a plaintext copy of real meetings: audio, transcript and
// summary, for as many meetings as the account may read. Treat the output
// directory as confidential and keep it out of version control.

// seedPackManifestVersion identifies the provenance document a pull leaves
// beside the archive. Versioned because a consumer that finds a shape it does
// not know must say so rather than guess.
const seedPackManifestVersion = "cassini.seed.pack.v1"

// seedPackCatalogName and seedPackManifestName are the pack's two index files.
// The catalog name is fixed by the format; the manifest name is ours.
const (
	seedPackCatalogName  = "catalog.json"
	seedPackManifestName = "seed-manifest.json"
)

// seedPackManifest records where a pack came from and what is in it.
//
// It exists so a directory found on disk months later can still answer "which
// instance, which account, when, and is this all of it" — questions the catalog
// alone cannot, because the catalog is the server's document and says nothing
// about the act of copying it.
//
// It deliberately holds no credential and no URL beyond the host.
type seedPackManifest struct {
	Version  string            `json:"version"`
	Source   seedPackSource    `json:"source"`
	PulledAt string            `json:"pulledAt"`
	Filter   string            `json:"filter,omitempty"`
	Limit    int               `json:"limit,omitempty"`
	Totals   seedPackTotals    `json:"totals"`
	Meetings []seedPackMeeting `json:"meetings"`
}

type seedPackSource struct {
	Host    string `json:"host"`
	Account string `json:"account"`
	// Origin is what the app said served the bytes. "nextcloud-files" means
	// per-caller permissions were applied; anything else means they were not,
	// and a pack built from such a run should not be described as an
	// access-controlled view of the archive.
	Origin string `json:"origin"`
}

type seedPackTotals struct {
	Meetings int   `json:"meetings"`
	Bytes    int64 `json:"bytes"`
}

type seedPackMeeting struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

// runDevMeetings routes the `dev meetings` group.
func runDevMeetings(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printDevMeetingsUsage(stdout)
		return 0
	}
	switch args[0] {
	case "help", "-h", "--help":
		printDevMeetingsUsage(stdout)
		return 0
	case "pull":
		return runDevMeetingsPull(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown dev meetings command %q\n\n", args[0])
		printDevMeetingsUsage(stderr)
		return 2
	}
}

func printDevMeetingsUsage(w io.Writer) {
	fmt.Fprint(w, `Mirror a Nextcloud meeting archive into a local seed pack.

Usage:
  cassini dev meetings pull --out <dir>
  cassini dev meetings pull --out <dir> --limit 2
  cassini dev meetings pull --out <dir> --room <room> --from 2026-08-01

Commands:
  pull   Download the meetings this account may read into a directory a
         development stack can be seeded from

The pack it writes is a published site root — catalog.json plus
meetings/<id>.opus — so `+"`cassini dev stack up --seed <dir>`"+` can load it, and so
can the operator's backfill.
`+"\n")
}

func runDevMeetingsPull(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var cfg meetingsConfig
	fs := flag.NewFlagSet("cassini dev meetings pull", flag.ContinueOnError)
	fs.SetOutput(stderr)
	registerMeetingsConnectionFlags(fs, &cfg)
	outDir := fs.String("out", "", "required directory to write the seed pack into")
	fromDate := fs.String("from", "", "only meetings on or after this date (e.g. 2026-08-01)")
	toDate := fs.String("to", "", "only meetings on or before this date (a bare date includes the whole day)")
	room := fs.String("room", "", "only meetings from this room, as printed by `cassini meetings rooms`")
	limit := fs.Int("limit", 0, "keep only the newest N of the selected meetings (0 means all of them)")
	dryRun := fs.Bool("dry-run", false, "report what would be pulled, and how many bytes, without writing anything")
	force := fs.Bool("force", false, "re-download meetings already present at the expected size")
	asJSON := fs.Bool("json", false, "write the manifest to stdout instead of a progress log")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini dev meetings pull --out ./harness/runtime/seed/prod
  cassini dev meetings pull --out ./harness/runtime/seed/two --limit 2
  cassini dev meetings pull --out ./harness/runtime/seed/prod --dry-run

Download every meeting this Nextcloud account may read into a seed pack: a
directory holding catalog.json and meetings/<id>.opus, plus a manifest saying
where the data came from.

By default it pulls everything the account can read. The filters narrow that
and combine; --limit keeps the newest N of whatever survives them. Run with
--dry-run first to see the size before committing to the transfer.

Re-running is cheap: a meeting already on disk at the size the server reports
is skipped, so an interrupted pull is resumed by repeating the command.

A pack contains real recordings — audio, transcripts and summaries. Keep the
directory out of version control and treat it as confidential.

`+"\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "pull does not accept positional arguments: %v\n", redactMeetingsArgs(fs.Args()))
		fs.Usage()
		return 2
	}
	if strings.TrimSpace(*outDir) == "" {
		fmt.Fprintf(stderr, "pull configuration error: --out is required\n")
		return 2
	}
	if *limit < 0 {
		fmt.Fprintf(stderr, "pull configuration error: --limit must not be negative, got %d\n", *limit)
		return 2
	}
	filter, err := parseMeetingsFilter(*fromDate, *toDate, *room)
	if err != nil {
		fmt.Fprintf(stderr, "pull configuration error: %v\n", err)
		return 2
	}
	if err := resolveMeetingsConfig(fs, &cfg); err != nil {
		fmt.Fprintf(stderr, "pull configuration error: %v\n", err)
		return 2
	}
	warnAboutInsecureTLS(stderr, cfg)

	client := newMeetingsClient(cfg)
	listing, err := client.fetchCatalog(ctx)
	if err != nil {
		return reportMeetingsError(stderr, "pull", cfg, err)
	}
	warnAboutMeetingsSource(stderr, listing)

	result := applyMeetingsFilter(listing.Items, filter)
	// The catalog arrives newest first, and the filter preserves that order, so
	// --limit is a prefix. Said out loud because "the newest N" is a promise the
	// caller is relying on, not a side effect of how the slice happens to be
	// ordered.
	selected := result.items
	if *limit > 0 && len(selected) > *limit {
		selected = selected[:*limit]
	}

	log := func(format string, args ...any) {
		if *asJSON {
			return
		}
		fmt.Fprintf(stdout, format+"\n", args...)
	}

	if len(selected) == 0 {
		fmt.Fprintf(stderr, "pull: nothing to pull — %s\n", explainEmptyPullSelection(listing, filter, result))
		return 1
	}

	catalogURL, err := client.catalogURL()
	if err != nil {
		return reportMeetingsError(stderr, "pull", cfg, err)
	}

	plans, err := planSeedPack(catalogURL, *outDir, selected)
	if err != nil {
		fmt.Fprintf(stderr, "pull failed: %v\n", err)
		return 1
	}

	host := meetingsHost(cfg.nextcloudURL)
	log("pulling %d meeting(s) from %s as %s", len(plans), host, cfg.user)
	if filter.active() {
		log("filter=%s excluded=%d", filter.describe(), result.excluded)
	}
	if *limit > 0 {
		log("limit=%d (newest first)", *limit)
	}

	if *dryRun {
		return reportSeedPackDryRun(ctx, client, plans, stdout, stderr, *asJSON)
	}

	if err := os.MkdirAll(filepath.Join(*outDir, "meetings"), 0o700); err != nil {
		fmt.Fprintf(stderr, "pull failed: create %s: %v\n", *outDir, err)
		return 1
	}

	var (
		pulled []seedPackMeeting
		total  int64
		cached int
		failed []string
	)
	for i, plan := range plans {
		bytes, wasCached, err := client.pullOneMeeting(ctx, plan, *force)
		if err != nil {
			failed = append(failed, plan.entry.ID)
			// Keep going. A pack missing four of 128 meetings is a smaller
			// corpus, not a wrong answer, and the catalog written at the end
			// names only what actually landed — so a partial pack is still
			// internally consistent and still seeds a stack.
			fmt.Fprintf(stderr, "[%*d/%d] %s FAILED: %v\n", len(fmt.Sprint(len(plans))), i+1, len(plans), plan.entry.ID, err)
			continue
		}
		state := "ok"
		if wasCached {
			state = "cached"
			cached++
		}
		log("[%*d/%d] %s %s %s", len(fmt.Sprint(len(plans))), i+1, len(plans), plan.entry.ID, humanBytes(bytes), state)
		pulled = append(pulled, seedPackMeeting{ID: plan.entry.ID, Path: plan.relPath, Bytes: bytes})
		total += bytes
	}

	if len(pulled) == 0 {
		fmt.Fprintf(stderr, "pull failed: every one of the %d selected meeting(s) failed to download; nothing was written\n", len(plans))
		return 1
	}

	// Index last, objects first — the ordering the publish sink and the
	// operator's backfill both use, for the same reason: a run that dies part
	// way leaves files nothing points at, never an index pointing at files that
	// are not there.
	if err := writeSeedPackCatalog(*outDir, listing.Version, plansForPulled(plans, pulled)); err != nil {
		fmt.Fprintf(stderr, "pull failed: write %s: %v\n", seedPackCatalogName, err)
		return 1
	}
	manifest := seedPackManifest{
		Version:  seedPackManifestVersion,
		Source:   seedPackSource{Host: host, Account: cfg.user, Origin: listing.Source},
		PulledAt: time.Now().UTC().Format(time.RFC3339),
		Filter:   filterDescriptionOrEmpty(filter),
		Limit:    *limit,
		Totals:   seedPackTotals{Meetings: len(pulled), Bytes: total},
		Meetings: pulled,
	}
	if err := writeSeedPackManifest(*outDir, manifest); err != nil {
		fmt.Fprintf(stderr, "pull failed: write %s: %v\n", seedPackManifestName, err)
		return 1
	}

	if *asJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(manifest); err != nil {
			fmt.Fprintf(stderr, "pull failed: write JSON: %v\n", err)
			return 1
		}
	} else {
		log("catalog -> %s", filepath.Join(*outDir, seedPackCatalogName))
		log("done: %d meeting(s), %s, %d cached, %d failed -> %s", len(pulled), humanBytes(total), cached, len(failed), *outDir)
	}
	if len(failed) > 0 {
		fmt.Fprintf(stderr, "pull: %d meeting(s) did not download: %s\n", len(failed), strings.Join(failed, " "))
		fmt.Fprintf(stderr, "the pack holds the %d that did and is usable as it stands; re-run to retry the rest\n", len(pulled))
		return 1
	}
	return 0
}

// seedPackPlan is one meeting's place in the pack: where to fetch it from and
// where it belongs on disk.
type seedPackPlan struct {
	entry    meetingsCatalogEntry
	raw      json.RawMessage
	audioURL *url.URL
	// relPath is the pack-relative slash path, exactly as the catalog entry
	// names it, so the pack's own catalog keeps pointing at the right file.
	relPath string
	// localPath is relPath resolved under --out, in native separators.
	localPath string
}

// planSeedPack turns the selected catalog items into a download plan, refusing
// anything whose asset path is not a plain relative path inside the pack.
//
// The catalog is network data. Its audioPath values decide both which URL is
// requested — already constrained to the published tree by
// resolveMeetingAudioURL — and which local file is written, which is this
// function's concern: an entry naming "../../.ssh/authorized_keys" must not be
// able to place a download outside the directory the caller asked for.
func planSeedPack(catalogURL *url.URL, outDir string, items []meetingsCatalogItem) ([]seedPackPlan, error) {
	plans := make([]seedPackPlan, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		audioURL, err := resolveMeetingAudioURL(catalogURL, item.entry)
		if err != nil {
			return nil, err
		}
		rel, err := packRelativeAsset(item.entry.AudioPath)
		if err != nil {
			return nil, fmt.Errorf("meeting %q: %w", item.entry.ID, err)
		}
		if seen[rel] {
			// Two entries naming one file would make the second overwrite the
			// first and the manifest double-count it. The catalog is the
			// server's document and nothing promises its ids are unique.
			return nil, fmt.Errorf("two catalog entries both name %s; refusing to write one meeting over another", rel)
		}
		seen[rel] = true
		plans = append(plans, seedPackPlan{
			entry:     item.entry,
			raw:       item.raw,
			audioURL:  audioURL,
			relPath:   rel,
			localPath: filepath.Join(outDir, filepath.FromSlash(rel)),
		})
	}
	return plans, nil
}

// packRelativeAsset normalises a catalog asset path into a slash path inside
// the pack, rejecting anything that is not one.
//
// Mirrors the rule the operator applies to a site root (siteRelativeAsset):
// absolute paths, URLs and anything climbing out with ".." are refused rather
// than cleaned into something plausible.
func packRelativeAsset(raw string) (string, error) {
	candidate := strings.TrimSpace(raw)
	if candidate == "" {
		return "", errors.New("the catalog entry names no audioPath, so there is nothing to pull")
	}
	if strings.Contains(candidate, "://") {
		return "", fmt.Errorf("audioPath %q is a URL, not a path inside the archive", candidate)
	}
	if strings.HasPrefix(candidate, "/") {
		return "", fmt.Errorf("audioPath %q is absolute, not a path inside the archive", candidate)
	}
	cleaned := path.Clean(strings.TrimPrefix(candidate, "./"))
	if cleaned == "." || cleaned == "/" {
		return "", fmt.Errorf("audioPath %q names no file", candidate)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("audioPath %q climbs out of the archive", candidate)
	}
	return cleaned, nil
}

// pullOneMeeting downloads one meeting unless an identical copy is already
// there, and reports the size and whether it was skipped.
//
// The size the server reports is asked for first, and used twice: to decide
// whether the local file is already the whole meeting, and to check that what
// arrived is the length that was promised. A transfer cut short mid-body is
// otherwise indistinguishable from a short meeting — which is exactly how a
// sibling tool in this repo ended up writing corrupt media (D-714).
func (c *meetingsClient) pullOneMeeting(ctx context.Context, plan seedPackPlan, force bool) (int64, bool, error) {
	expected, err := c.meetingContentLength(ctx, plan.audioURL)
	if err != nil {
		return 0, false, err
	}
	if !force && expected > 0 {
		if info, statErr := os.Stat(plan.localPath); statErr == nil && !info.IsDir() && info.Size() == expected {
			return info.Size(), true, nil
		}
	}
	written, err := c.downloadMeeting(ctx, plan.audioURL, plan.localPath)
	if err != nil {
		return 0, false, err
	}
	if expected > 0 && written != expected {
		// downloadMeeting has already renamed the file into place, so remove it:
		// leaving a short .opus at the destination would let the next run's size
		// check treat it as cached and make the truncation permanent.
		_ = os.Remove(plan.localPath)
		return 0, false, fmt.Errorf("downloaded %d bytes but the server said %d; the transfer was cut short", written, expected)
	}
	return written, false, nil
}

// meetingContentLength asks how big a meeting is without downloading it.
//
// Returns -1 when the server does not say. That is not an error: the download
// still works, it simply cannot be resumed by size or checked against a
// promised length, and every caller here treats a negative length as "unknown"
// rather than as zero.
func (c *meetingsClient) meetingContentLength(ctx context.Context, target *url.URL) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, target.String(), nil)
	if err != nil {
		return -1, err
	}
	req.SetBasicAuth(c.cfg.user, c.cfg.appPassword)
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := c.stream.Do(req)
	if err != nil {
		return -1, fmt.Errorf("HEAD %s: %w", target.Redacted(), err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return -1, &meetingsHTTPError{URL: target.Redacted(), Status: resp.StatusCode}
	}
	if resp.ContentLength < 0 {
		return -1, nil
	}
	return resp.ContentLength, nil
}

// reportSeedPackDryRun prints the selection and its total size, writing nothing.
func reportSeedPackDryRun(ctx context.Context, client *meetingsClient, plans []seedPackPlan, stdout, stderr io.Writer, asJSON bool) int {
	var (
		total   int64
		unknown int
		sizes   = make([]seedPackMeeting, 0, len(plans))
	)
	for _, plan := range plans {
		size, err := client.meetingContentLength(ctx, plan.audioURL)
		if err != nil {
			fmt.Fprintf(stderr, "dry run: %s: %v\n", plan.entry.ID, err)
			return 1
		}
		if size < 0 {
			unknown++
		} else {
			total += size
		}
		sizes = append(sizes, seedPackMeeting{ID: plan.entry.ID, Path: plan.relPath, Bytes: size})
	}
	if asJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(struct {
			DryRun   bool              `json:"dryRun"`
			Totals   seedPackTotals    `json:"totals"`
			Meetings []seedPackMeeting `json:"meetings"`
		}{true, seedPackTotals{Meetings: len(sizes), Bytes: total}, sizes}); err != nil {
			fmt.Fprintf(stderr, "dry run failed: write JSON: %v\n", err)
			return 1
		}
		return 0
	}
	for _, m := range sizes {
		fmt.Fprintf(stdout, "  would pull %s  %s\n", m.ID, humanBytes(m.Bytes))
	}
	fmt.Fprintf(stdout, "dry run: %d meeting(s), %s, nothing written\n", len(sizes), humanBytes(total))
	if unknown > 0 {
		fmt.Fprintf(stdout, "note=%d meeting(s) reported no size, so the total above is a lower bound\n", unknown)
	}
	return 0
}

// plansForPulled returns the plans whose meetings actually landed on disk, in
// the catalog's own order.
//
// The pack's catalog must name only files that are present: a seeder — this
// repo's own, and the operator's backfill — treats a named-but-absent asset as
// a broken archive, which a partially successful pull would otherwise produce.
func plansForPulled(plans []seedPackPlan, pulled []seedPackMeeting) []seedPackPlan {
	present := make(map[string]bool, len(pulled))
	for _, m := range pulled {
		present[m.Path] = true
	}
	kept := make([]seedPackPlan, 0, len(pulled))
	for _, plan := range plans {
		if present[plan.relPath] {
			kept = append(kept, plan)
		}
	}
	return kept
}

// writeSeedPackCatalog writes the pack's index, re-emitting each entry exactly
// as the server sent it.
//
// Verbatim on purpose. The entries carry fields this build may not know about,
// and every consumer downstream — the viewer, the operator, the CLI itself —
// reads the producer's shape. Re-normalising them here would make the pack a
// second, subtly different dialect of a format that already has one.
func writeSeedPackCatalog(outDir, version string, plans []seedPackPlan) error {
	meetings := make([]json.RawMessage, 0, len(plans))
	for _, plan := range plans {
		meetings = append(meetings, plan.raw)
	}
	body, err := json.MarshalIndent(struct {
		Version  string            `json:"version"`
		Meetings []json.RawMessage `json:"meetings"`
	}{version, meetings}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(outDir, seedPackCatalogName), append(body, '\n'), 0o600)
}

func writeSeedPackManifest(outDir string, manifest seedPackManifest) error {
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(outDir, seedPackManifestName), append(body, '\n'), 0o600)
}

func filterDescriptionOrEmpty(filter meetingsFilter) string {
	if !filter.active() {
		return ""
	}
	return filter.describe()
}

// explainEmptyPullSelection says why there is nothing to pull, distinguishing
// the three causes that need three different responses: the account reads
// nothing, the filter removed everything, or the catalog was unusable.
func explainEmptyPullSelection(listing meetingsListing, filter meetingsFilter, result meetingsFilterResult) string {
	switch {
	case filter.active() && result.excluded > 0:
		return fmt.Sprintf("your filter excluded all %d readable meeting(s); widen it, or run `cassini meetings rooms` to see what to filter by", result.excluded)
	case listing.Skipped > 0:
		return fmt.Sprintf("every one of the %d catalog entries was unusable, so the archive is readable but malformed", listing.Skipped)
	default:
		return "this account can read no meetings at all, which also looks like a recordings folder that was never provisioned; check the same account in the Cassini viewer"
	}
}

// meetingsHost returns the host of a normalised Nextcloud base URL, for
// printing. A parse failure falls back to the whole value, which
// normalizeNextcloudURL has already stripped of any userinfo.
func meetingsHost(nextcloudURL string) string {
	parsed, err := url.Parse(nextcloudURL)
	if err != nil || parsed.Host == "" {
		return nextcloudURL
	}
	return parsed.Host
}

// humanBytes renders a size for a progress line. A negative size means the
// server did not say.
func humanBytes(n int64) string {
	if n < 0 {
		return "size unknown"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for size := n / unit; size >= unit; size /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
