package operator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// D-737: a rerun re-delivers a recording without erasing the marks written on
// the delivered copy since it was last published.

const carryOpus = "Cassini/Recordings/meetings/meeting-a.opus"

// wireRequest is one request as it crossed the wire to the fake Nextcloud.
type wireRequest struct {
	method, path, ifMatch string
}

// wiredNC fronts fakeNCFiles with a log of EVERY request — GETs included, which
// the fake's own op log leaves out because they change nothing, and which are
// the whole difference between a re-delivery that carries marks and one that
// does not — plus a concurrent writer that can be told to win races.
type wiredNC struct {
	*fakeNCFiles
	url string

	wireMu sync.Mutex
	wire   []wireRequest
	// races is how many conditional PUTs to racePath find the leaf already
	// rewritten by somebody else: a mark committed between our read and our
	// write. Each appends " +theirs" and moves the ETag on, as a commit does.
	racePath string
	races    int
}

func newWiredNC(t *testing.T) *wiredNC {
	t.Helper()
	w := &wiredNC{fakeNCFiles: newFakeNCFiles()}
	inner := w.fakeNCFiles.server(t).Config.Handler
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rel := r.URL.Path
		if idx := strings.Index(rel, "/Cassini"); idx >= 0 {
			rel = rel[idx+1:]
		}
		ifMatch := r.Header.Get("If-Match")
		w.wireMu.Lock()
		w.wire = append(w.wire, wireRequest{method: r.Method, path: rel, ifMatch: ifMatch})
		race := r.Method == http.MethodPut && ifMatch != "" && rel == w.racePath && w.races > 0
		if race {
			w.races--
		}
		w.wireMu.Unlock()
		if race {
			w.mu.Lock()
			w.files[rel] = append(append([]byte{}, w.files[rel]...), " +theirs"...)
			w.etags[rel]++
			w.mu.Unlock()
		}
		inner.ServeHTTP(rw, r)
	}))
	t.Cleanup(srv.Close)
	w.url = srv.URL
	return w
}

// mark is a position in the wire log, so a test can look at one delivery.
func (w *wiredNC) mark() int {
	w.wireMu.Lock()
	defer w.wireMu.Unlock()
	return len(w.wire)
}

// sequenceSince renders the requests to path from position from on, as "METHOD"
// or, for a conditional write, "PUT If-Match <etag>".
func (w *wiredNC) sequenceSince(from int, path string) []string {
	w.wireMu.Lock()
	defer w.wireMu.Unlock()
	var out []string
	for _, r := range w.wire[from:] {
		if r.path != path {
			continue
		}
		s := r.method
		if r.ifMatch != "" {
			s += " If-Match " + r.ifMatch
		}
		out = append(out, s)
	}
	return out
}

func (w *wiredNC) raceNext(path string, n int) {
	w.wireMu.Lock()
	defer w.wireMu.Unlock()
	w.racePath, w.races = path, n
}

// markDelivered stands in for the write endpoint committing a mark onto the
// delivered copy: new bytes and a new ETag. It answers the ETag's counter.
func (w *wiredNC) markDelivered(path, mark string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.files[path] = append(append([]byte{}, w.files[path]...), mark...)
	w.etags[path]++
	return w.etags[path]
}

func (w *wiredNC) content(path string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.files[path])
}

// ncETag is the fake's ETag for a counter, quoted as Nextcloud quotes its own.
func ncETag(n int) string { return fmt.Sprintf("%q", strconv.Itoa(n)) }

// fakeAnnotateCLI answers `cassini annotate carry` and `show` as the real CLI
// does, in miniature. carry writes the sealed bytes followed by
// " carrying[<the delivered copy's bytes>]", so the PUT body says both what was
// sealed and which delivered copy was carried; show reads anything non-empty.
// An empty file is not a recording to either, as a zero-byte reservation is not
// to the real one. Every call is logged beside the script.
type fakeAnnotateCLI struct{ bin string }

type fakeAnnotateOptions struct {
	unresolved bool // carry reports the marks bound to different audio
	carryFails bool // carry refuses a delivered copy that show can read
	showFails  bool // show refuses every file
}

func newFakeAnnotateCLI(t *testing.T, opts fakeAnnotateOptions) *fakeAnnotateCLI {
	t.Helper()
	resolved := "true"
	if opts.unresolved {
		resolved = "false"
	}
	carryFail, showFail := "", ""
	if opts.carryFails {
		carryFail = `echo "annotations.items[0]: tagId names no tag" >&2; exit 4`
	}
	if opts.showFails {
		showFail = `echo "cannot read the manifest" >&2; exit 1`
	}
	body := `
case "$2" in
carry)
  printf '%s|%s|%s\n' "$3" "$4" "$6" >> "$0.carry"
  if [ ! -s "$3" ]; then echo "not an Ogg Opus stream" >&2; exit 1; fi
  ` + carryFail + `
  { cat "$4"; printf ' carrying['; cat "$3"; printf ']'; } > "$6"
  printf '{"format":"cassini.annotate.result.v1","annotations":{"format":"cassini.annotations.v1"},"revision":2,"carried":1,"resolved":` + resolved + `,"audioOpusSha256":"audio-1","containerSha256":"staged-digest"}'
  ;;
show)
  printf '%s\n' "$3" >> "$0.show"
  ` + showFail + `
  if [ ! -s "$3" ]; then echo "not an Ogg Opus stream" >&2; exit 1; fi
  printf '{"format":"cassini.annotate.result.v1","annotations":null,"revision":0,"resolved":true,"audioOpusSha256":"audio-1","containerSha256":"sealed-digest"}'
  ;;
*) exit 2 ;;
esac`
	return &fakeAnnotateCLI{bin: fakeCassini(t, body)}
}

// calls lists one verb's invocations, in order.
func (c *fakeAnnotateCLI) calls(t *testing.T, verb string) []string {
	t.Helper()
	raw, err := os.ReadFile(c.bin + "." + verb)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// annotationIndexSpy records what the sink tells the projection.
type annotationIndexSpy struct {
	mu          sync.Mutex
	recorded    []string // "<opusName> <containerSha256>"
	unavailable []string // "<opusName> <reason>"
	recordErr   error
}

func (s *annotationIndexSpy) Record(_ context.Context, opusName string, result annotateResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recorded = append(s.recorded, opusName+" "+result.ContainerSHA256)
	return s.recordErr
}

func (s *annotationIndexSpy) MarkUnavailable(_ context.Context, opusName, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unavailable = append(s.unavailable, opusName+" "+reason)
	return nil
}

func (s *annotationIndexSpy) ResolveLabel(context.Context, string, []string) (string, bool, error) {
	return "", false, nil
}

func (s *annotationIndexSpy) Namespace(context.Context) (string, error) { return "", nil }

// newCarryingSink is newNCSink with the CLI wired in (nil: none configured) and
// an audience applier that counts, so the access invariant can be asserted.
func newCarryingSink(t *testing.T, w *wiredNC, cli *fakeAnnotateCLI) (*nextcloudFilesPublishSink, *int) {
	t.Helper()
	sink := newNCSink(t, w.url)
	if cli != nil {
		sink.cassiniBin = cli.bin
	}
	applied := 0
	sink.applyAccess = func(ctx context.Context, jobID string) error {
		applied++
		return sink.cfg.davProppatchACLRules(ctx, sink.client, ncRecordingsOwner,
			ncACLRecordingsRoot+"/meetings/"+jobID+".opus",
			recordingACLRules([]aclMapping{{Type: "user", ID: "alice"}}, false))
	}
	return sink, &applied
}

func publishMeetingA(t *testing.T, sink *nextcloudFilesPublishSink, name string) (attempt string, err error) {
	t.Helper()
	attempt = writeAttemptSite(t, filepath.Join(t.TempDir(), name), "meeting-a")
	_, err = deliverToNC(t, sink, attempt, "meeting-a")
	return attempt, err
}

func assertSequence(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Fatalf("%s: requests = %v, want %v", what, got, want)
	}
}

func TestNCSinkRerunCarriesTheDeliveredMarksIntoTheSealedFile(t *testing.T) {
	w := newWiredNC(t)
	cli := newFakeAnnotateCLI(t, fakeAnnotateOptions{})
	sink, applied := newCarryingSink(t, w, cli)

	// A first publish has nothing delivered to carry from: no fetch, no carry, no
	// condition on the write — the D-594 sequence exactly as it always was.
	if _, err := publishMeetingA(t, sink, "one"); err != nil {
		t.Fatalf("first Deliver() error = %v", err)
	}
	assertSequence(t, "first publish", w.sequenceSince(0, carryOpus),
		"PROPFIND", "PUT", "PROPPATCH", "PUT", "PROPFIND", "PROPPATCH")
	if got := cli.calls(t, "carry"); len(got) != 0 {
		t.Fatalf("a first publish ran carry: %v", got)
	}

	// Somebody marks the meeting: the delivered copy is rewritten in place.
	etag := w.markDelivered(carryOpus, " +mark")
	aclWrites := len(w.aclBodiesFor(carryOpus))

	from := w.mark()
	rerun, err := publishMeetingA(t, sink, "two")
	if err != nil {
		t.Fatalf("rerun Deliver() error = %v", err)
	}
	sealed := filepath.Join(rerun, "meetings", "meeting-a.opus")

	// The health gate, then the delivered copy fetched (the GET is D-737's: it
	// is carry's input), then the staged copy written only over the version
	// whose marks were read, then the read-back.
	assertSequence(t, "rerun", w.sequenceSince(from, carryOpus),
		"PROPFIND", "GET", "PUT If-Match "+ncETag(etag), "PROPFIND")

	carries := cli.calls(t, "carry")
	if len(carries) != 1 {
		t.Fatalf("carry ran %d times, want 1: %v", len(carries), carries)
	}
	args := strings.Split(carries[0], "|") // delivered, sealed, staged
	if args[1] != sealed {
		t.Errorf("carry's sealed input = %q, want the job's sealed file %q", args[1], sealed)
	}
	if args[0] == sealed || args[2] == sealed || args[0] == args[2] {
		t.Errorf("carry must read a fetched copy and write a third file, never the sealed one: %v", args)
	}

	// What was PUT is the staged copy — the sealed audio carrying the delivered
	// copy's marks — and the size read-back accepted it, which it does only
	// when it compares against the staged file rather than the sealed one.
	if got, want := w.content(carryOpus), "opus-meeting-a carrying[opus-meeting-a +mark]"; got != want {
		t.Fatalf("the archive holds %q, want %q", got, want)
	}
	if got, _ := os.ReadFile(sealed); string(got) != "opus-meeting-a" {
		t.Errorf("the sealed file was modified: %q", got)
	}

	// Content, never access: no audience re-derived, no rule written.
	if *applied != 1 {
		t.Errorf("audience applied %d times, want 1 — a re-delivery must not rewrite access", *applied)
	}
	if after := len(w.aclBodiesFor(carryOpus)); after != aclWrites {
		t.Errorf("the rerun sent %d ACL writes, want 0", after-aclWrites)
	}
	assertLeafProtected(t, w.fakeNCFiles, carryOpus)

	// The fetched and staged copies do not outlive the delivery (D-550).
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(sealed), ".carry-*")); len(leftovers) != 0 {
		t.Errorf("carry scratch left behind: %v", leftovers)
	}
}

// A mark committed between the fetch and the PUT must not be overwritten by a
// staged copy that never saw it: the PUT is refused, and the rerun reads the
// recording again — the other writer's mark with it.
func TestNCSinkRerunReadsAgainWhenAMarkLandsMidDelivery(t *testing.T) {
	w := newWiredNC(t)
	cli := newFakeAnnotateCLI(t, fakeAnnotateOptions{})
	sink, _ := newCarryingSink(t, w, cli)
	if _, err := publishMeetingA(t, sink, "one"); err != nil {
		t.Fatalf("first Deliver() error = %v", err)
	}
	etag := w.markDelivered(carryOpus, " +mark")
	w.raceNext(carryOpus, 1)

	from := w.mark()
	if _, err := publishMeetingA(t, sink, "two"); err != nil {
		t.Fatalf("rerun Deliver() error = %v", err)
	}
	assertSequence(t, "rerun", w.sequenceSince(from, carryOpus),
		"PROPFIND", "GET", "PUT If-Match "+ncETag(etag),
		"PROPFIND", "GET", "PUT If-Match "+ncETag(etag+1),
		"PROPFIND")
	if got, want := w.content(carryOpus), "opus-meeting-a carrying[opus-meeting-a +mark +theirs]"; got != want {
		t.Fatalf("the archive holds %q, want both marks carried: %q", got, want)
	}
	if got := len(cli.calls(t, "carry")); got != 2 {
		t.Errorf("carry ran %d times, want 2", got)
	}
}

// Three refusals in a row: something is writing marks continuously. The publish
// fails, loudly and retryably, and nothing of the rerun lands over them.
func TestNCSinkRerunGivesUpAfterThreeConflicts(t *testing.T) {
	w := newWiredNC(t)
	cli := newFakeAnnotateCLI(t, fakeAnnotateOptions{})
	sink, _ := newCarryingSink(t, w, cli)
	if _, err := publishMeetingA(t, sink, "one"); err != nil {
		t.Fatalf("first Deliver() error = %v", err)
	}
	etag := w.markDelivered(carryOpus, " +mark")
	w.raceNext(carryOpus, 3)

	from := w.mark()
	_, err := publishMeetingA(t, sink, "two")
	if err == nil {
		t.Fatal("a rerun that lost three races to mark writes reported success")
	}
	if !errors.Is(err, errDAVPreconditionFailed) || !strings.Contains(err.Error(), "re-run the publish") {
		t.Errorf("the failure must say it is contention and that re-running is the fix: %v", err)
	}
	assertSequence(t, "rerun", w.sequenceSince(from, carryOpus),
		"PROPFIND", "GET", "PUT If-Match "+ncETag(etag),
		"PROPFIND", "GET", "PUT If-Match "+ncETag(etag+1),
		"PROPFIND", "GET", "PUT If-Match "+ncETag(etag+2))
	if got, want := w.content(carryOpus), "opus-meeting-a +mark +theirs +theirs +theirs"; got != want {
		t.Fatalf("the archive holds %q, want every mark kept and nothing of the rerun: %q", got, want)
	}
	if got := w.sequenceSince(from, "Cassini/Recordings/catalog.json"); strings.Contains(strings.Join(got, ","), "PUT") {
		t.Errorf("a failed rerun rewrote the catalog: %v", got)
	}
}

// Marks made against different audio are kept, flagged, and delivered — never
// dropped (Silvio's rule).
func TestNCSinkRerunDeliversUnresolvedMarks(t *testing.T) {
	w := newWiredNC(t)
	cli := newFakeAnnotateCLI(t, fakeAnnotateOptions{unresolved: true})
	sink, _ := newCarryingSink(t, w, cli)
	var logs bytes.Buffer
	sink.logger = log.New(&logs, "", 0)
	if _, err := publishMeetingA(t, sink, "one"); err != nil {
		t.Fatalf("first Deliver() error = %v", err)
	}
	w.markDelivered(carryOpus, " +mark")

	if _, err := publishMeetingA(t, sink, "two"); err != nil {
		t.Fatalf("unresolved marks must not fail a rerun: %v", err)
	}
	if got, want := w.content(carryOpus), "opus-meeting-a carrying[opus-meeting-a +mark]"; got != want {
		t.Fatalf("the archive holds %q, want the carried copy %q", got, want)
	}
	if !strings.Contains(logs.String(), "different audio") {
		t.Errorf("an unresolved carry must be logged; log was:\n%s", logs.String())
	}
}

// With no CLI there is nothing to carry with — and nothing to carry, because the
// routes that write marks are not mounted there. Delivery is exactly what it was
// before D-737, and says so once.
func TestNCSinkWithoutACLIRedeliversAsBefore(t *testing.T) {
	w := newWiredNC(t)
	sink, _ := newCarryingSink(t, w, nil)
	var logs bytes.Buffer
	sink.logger = log.New(&logs, "", 0)
	if _, err := publishMeetingA(t, sink, "one"); err != nil {
		t.Fatalf("first Deliver() error = %v", err)
	}
	w.markDelivered(carryOpus, " +mark")

	from := w.mark()
	if _, err := publishMeetingA(t, sink, "two"); err != nil {
		t.Fatalf("rerun Deliver() error = %v", err)
	}
	assertSequence(t, "rerun", w.sequenceSince(from, carryOpus), "PROPFIND", "PUT", "PROPFIND")
	if got := w.content(carryOpus); got != "opus-meeting-a" {
		t.Fatalf("the archive holds %q, want the sealed file", got)
	}
	if _, err := publishMeetingA(t, sink, "three"); err != nil {
		t.Fatalf("second rerun Deliver() error = %v", err)
	}
	if n := strings.Count(logs.String(), "no cassini binary configured"); n != 1 {
		t.Errorf("the no-CLI notice was logged %d times, want once:\n%s", n, logs.String())
	}
}

// The projection hears what each delivery left in the archive, keyed by the
// recording's basename: a first publish's sealed file read where it lies, a
// rerun's staged copy as carry reported it.
func TestNCSinkRecordsWhatTheDeliveredRecordingCarries(t *testing.T) {
	w := newWiredNC(t)
	cli := newFakeAnnotateCLI(t, fakeAnnotateOptions{})
	sink, _ := newCarryingSink(t, w, cli)
	spy := &annotationIndexSpy{}
	sink.annotations = func() annotationIndex { return spy }

	attempt, err := publishMeetingA(t, sink, "one")
	if err != nil {
		t.Fatalf("first Deliver() error = %v", err)
	}
	if got := strings.Join(spy.recorded, ","); got != "meeting-a.opus sealed-digest" {
		t.Fatalf("recorded %q after a first publish, want the sealed file's report", got)
	}
	if shows := cli.calls(t, "show"); len(shows) != 1 || shows[0] != filepath.Join(attempt, "meetings", "meeting-a.opus") {
		t.Fatalf("show calls = %v, want one, on the sealed file (never a fetched-back copy)", shows)
	}

	w.markDelivered(carryOpus, " +mark")
	if _, err := publishMeetingA(t, sink, "two"); err != nil {
		t.Fatalf("rerun Deliver() error = %v", err)
	}
	if got := strings.Join(spy.recorded, ","); got != "meeting-a.opus sealed-digest,meeting-a.opus staged-digest" {
		t.Fatalf("recorded %q after a rerun, want carry's report of the staged copy", got)
	}
	if n := len(cli.calls(t, "show")); n != 1 {
		t.Errorf("show ran %d times, want still 1 — a rerun already has carry's report", n)
	}
}

// The projection is rebuildable; a publish is not a thing it may fail.
func TestNCSinkNeverFailsAPublishOverTheMarksIndex(t *testing.T) {
	t.Run("marks unreadable", func(t *testing.T) {
		w := newWiredNC(t)
		sink, _ := newCarryingSink(t, w, newFakeAnnotateCLI(t, fakeAnnotateOptions{showFails: true}))
		spy := &annotationIndexSpy{}
		sink.annotations = func() annotationIndex { return spy }
		if _, err := publishMeetingA(t, sink, "one"); err != nil {
			t.Fatalf("an unreadable index entry failed the publish: %v", err)
		}
		if got := strings.Join(spy.unavailable, ","); got != "meeting-a.opus "+publishAnnotationsUnreadable {
			t.Errorf("unavailable = %q, want the meeting recorded as not covered", got)
		}
	})
	t.Run("record fails", func(t *testing.T) {
		w := newWiredNC(t)
		sink, _ := newCarryingSink(t, w, newFakeAnnotateCLI(t, fakeAnnotateOptions{}))
		spy := &annotationIndexSpy{recordErr: errors.New("disk I/O error")}
		sink.annotations = func() annotationIndex { return spy }
		if _, err := publishMeetingA(t, sink, "one"); err != nil {
			t.Fatalf("a failed index write failed the publish: %v", err)
		}
		if len(spy.unavailable) != 1 {
			t.Errorf("unavailable = %v, want the meeting recorded as not covered", spy.unavailable)
		}
	})
	t.Run("no index", func(t *testing.T) {
		w := newWiredNC(t)
		cli := newFakeAnnotateCLI(t, fakeAnnotateOptions{})
		sink, _ := newCarryingSink(t, w, cli)
		sink.annotations = func() annotationIndex { return nil }
		if _, err := publishMeetingA(t, sink, "one"); err != nil {
			t.Fatalf("Deliver() error = %v", err)
		}
		if shows := cli.calls(t, "show"); len(shows) != 0 {
			t.Errorf("read marks for an index that does not exist: %v", shows)
		}
	})
}

// A first publish that died between the reservation and the content leaves a
// protected, EMPTY leaf. That is no recording and carries no marks, so the rerun
// delivers the sealed file over it — still conditionally — and finishes the
// audience. Failing on it instead would wedge the meeting forever.
func TestNCSinkRerunOverAnEmptyReservationDeliversTheSealedFile(t *testing.T) {
	w := newWiredNC(t)
	cli := newFakeAnnotateCLI(t, fakeAnnotateOptions{})
	sink, applied := newCarryingSink(t, w, cli)
	w.mu.Lock()
	w.files[carryOpus] = []byte{}
	w.acls[carryOpus] = recordingACLRules(nil, false)
	etag := w.etagFor(carryOpus)
	w.mu.Unlock()

	if _, err := publishMeetingA(t, sink, "one"); err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	assertSequence(t, "rerun", w.sequenceSince(0, carryOpus),
		"PROPFIND", "GET", "PUT If-Match "+etag, "PROPFIND", "PROPPATCH")
	if got := w.content(carryOpus); got != "opus-meeting-a" {
		t.Fatalf("the archive holds %q, want the sealed file", got)
	}
	if *applied != 1 {
		t.Errorf("audience applied %d times, want 1 — the unfinished publish must be finished", *applied)
	}
}

// Any other carry failure fails the publish: delivering past it is the silent
// loss of every mark this exists to prevent.
func TestNCSinkRerunFailsWhenCarryRefusesAReadableCopy(t *testing.T) {
	w := newWiredNC(t)
	sink, _ := newCarryingSink(t, w, newFakeAnnotateCLI(t, fakeAnnotateOptions{carryFails: true}))
	if _, err := publishMeetingA(t, sink, "one"); err != nil {
		t.Fatalf("first Deliver() error = %v", err)
	}
	w.markDelivered(carryOpus, " +mark")

	from := w.mark()
	_, err := publishMeetingA(t, sink, "two")
	if err == nil || !strings.Contains(err.Error(), "carry the marks") {
		t.Fatalf("Deliver() error = %v, want the carry failure", err)
	}
	if got := w.content(carryOpus); got != "opus-meeting-a +mark" {
		t.Fatalf("the archive holds %q, want the marked copy untouched", got)
	}
	for _, r := range w.sequenceSince(from, carryOpus) {
		if strings.HasPrefix(r, "PUT") {
			t.Fatalf("a failed carry still wrote the recording: %v", w.sequenceSince(from, carryOpus))
		}
	}
}

// The default model has no health gate and no rules, but marks are written there
// too, so a rerun asks whether a delivered copy exists and carries from it —
// with no PROPPATCH anywhere, which that model rejects.
func TestDefaultModeRerunCarriesTheDeliveredMarks(t *testing.T) {
	setStorageMode(t, false)
	w := newWiredNC(t)
	cli := newFakeAnnotateCLI(t, fakeAnnotateOptions{})
	sink, _ := newCarryingSink(t, w, cli)
	opus := ncDefaultRecordingsRoot + "/meetings/meeting-a.opus"

	if _, err := publishMeetingA(t, sink, "one"); err != nil {
		t.Fatalf("first Deliver() error = %v", err)
	}
	// One PROPFIND more than before D-737, to learn there is nothing to carry.
	assertSequence(t, "first publish", w.sequenceSince(0, opus), "PROPFIND", "PUT", "PROPFIND")

	etag := w.markDelivered(opus, " +mark")
	from := w.mark()
	if _, err := publishMeetingA(t, sink, "two"); err != nil {
		t.Fatalf("rerun Deliver() error = %v", err)
	}
	assertSequence(t, "rerun", w.sequenceSince(from, opus),
		"PROPFIND", "GET", "PUT If-Match "+ncETag(etag), "PROPFIND")
	if got, want := w.content(opus), "opus-meeting-a carrying[opus-meeting-a +mark]"; got != want {
		t.Fatalf("the archive holds %q, want %q", got, want)
	}
	for _, op := range ncOpsByMethod(w.fakeNCFiles, "PROPPATCH") {
		t.Errorf("the default model PROPPATCHed %s", op.path)
	}
}

// The repair branch deletes the leaf it repairs, and every mark on it with it
// unless they are carried out first.
func TestNCSinkRepairCarriesTheMarksOutBeforeDeletingTheLeaf(t *testing.T) {
	w := newWiredNC(t)
	cli := newFakeAnnotateCLI(t, fakeAnnotateOptions{})
	sink, _ := newCarryingSink(t, w, cli)
	w.mu.Lock()
	w.files[carryOpus] = []byte("leaked +mark") // delivered, marked, and carrying no rule
	w.mu.Unlock()

	if _, err := publishMeetingA(t, sink, "one"); err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	// Fetch, then deny, DELETE, reserve, deny, and the carried content — its PUT
	// unconditional, because the leaf is now the reservation this delivery made.
	assertSequence(t, "repair", w.sequenceSince(0, carryOpus),
		"PROPFIND", "GET", "PROPPATCH", "DELETE", "PUT", "PROPPATCH", "PUT", "PROPFIND", "PROPPATCH")
	if got, want := w.content(carryOpus), "opus-meeting-a carrying[leaked +mark]"; got != want {
		t.Fatalf("the archive holds %q, want %q", got, want)
	}
	assertLeafProtected(t, w.fakeNCFiles, carryOpus)
}

// A carry that keeps refusing — marks in a format an older build does not know,
// after a rollback — must not leave the recording readable by every account on
// every rerun. The leaf is denied, keeps its marks, and the publish fails.
func TestNCSinkRepairDeniesTheLeafWhenItsMarksCannotBeCarried(t *testing.T) {
	w := newWiredNC(t)
	sink, applied := newCarryingSink(t, w, newFakeAnnotateCLI(t, fakeAnnotateOptions{carryFails: true}))
	w.mu.Lock()
	w.files[carryOpus] = []byte("leaked +mark") // delivered, marked, and carrying no rule
	w.mu.Unlock()

	for _, run := range []string{"one", "two"} {
		_, err := publishMeetingA(t, sink, run)
		if err == nil || !strings.Contains(err.Error(), "carry the marks") {
			t.Fatalf("publish %s: Deliver() error = %v, want the carry failure", run, err)
		}
		assertLeafProtected(t, w.fakeNCFiles, carryOpus)
		if got := w.content(carryOpus); got != "leaked +mark" {
			t.Fatalf("publish %s: the archive holds %q, want the marked copy kept", run, got)
		}
	}
	// Denied on the first publish; the second finds it protected and fails at
	// the carry again, touching nothing.
	assertSequence(t, "reruns", w.sequenceSince(0, carryOpus), "PROPFIND", "GET", "PROPPATCH", "PROPFIND", "GET")
	if *applied != 0 {
		t.Errorf("audience applied %d times over a failed publish", *applied)
	}
}
