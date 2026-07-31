package operator

import (
	"context"
	"fmt"
	"log"
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

// localPublishSink promotes the attempt site into the operator's site root —
// the behaviour publishing has always had on this machine.
type localPublishSink struct {
	siteRoot string
	logger   *log.Logger
}

func (s *localPublishSink) Name() string { return publishSinkLocal }

func (s *localPublishSink) Deliver(_ context.Context, d publishDelivery) (string, error) {
	if err := promoteSiteBundle(d.AttemptSitePath, s.siteRoot, SiteBundleLineage{
		JobID:          d.JobID,
		AttemptNumber:  d.AttemptNumber,
		PublishedAtUTC: d.PublishedAtUTC,
	}); err != nil {
		return "", err
	}
	return s.siteRoot, nil
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
