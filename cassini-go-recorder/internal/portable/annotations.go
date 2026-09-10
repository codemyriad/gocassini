package portable

import (
	"bytes"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Annotations is the optional `annotations` member of a published manifest: the
// tags people and agents put on a meeting, and on stretches of it (D-737).
//
// Manifest carries the member as raw JSON rather than as this type, so that no
// reader ever fails to open a recording because of it. ParseAnnotations is the
// reader's entry point and is tolerant; ValidateAnnotations is the writer's
// check and is strict. Unknown fields inside a v1 document are dropped by a
// typed round trip — v1 is ours to define, and a change that needs new fields
// is a new format string.
type Annotations struct {
	Format string `json:"format"`
	// Revision counts committed batches. It is informational: concurrent
	// writers are serialised by a conditional write on the file, not by this
	// number — a counter inside the file cannot prevent a lost update.
	Revision int `json:"revision"`
	// AudioOpusSHA256 binds every time range to the audio it was made against.
	// It is set from integrity.opusAudioSha256 on the first write. When the two
	// differ the marks are unresolved: made against different audio, and not to
	// be drawn against this one.
	AudioOpusSHA256 string `json:"audioOpusSha256"`
	// TagNamespace scopes tag ids, so the same tag across recordings is the same
	// (namespace, id). Set on a file's first write and never changed.
	TagNamespace string           `json:"tagNamespace"`
	Tags         []AnnotationTag  `json:"tags"`
	Items        []AnnotationItem `json:"items"`
}

// AnnotationTag defines a tag within one document.
type AnnotationTag struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// AnnotationItem is one mark: one application of a tag to a target.
type AnnotationItem struct {
	ID           string           `json:"id"`
	TagID        string           `json:"tagId"`
	Target       AnnotationTarget `json:"target"`
	CreatedAtUTC string           `json:"createdAtUtc"`
	Actor        AnnotationActor  `json:"actor"`
	// OperationID groups the items one batch added, so a whole run — an
	// agent's, typically — can be undone in one step.
	OperationID string `json:"operationId"`
}

// AnnotationTarget is what a mark applies to. A meeting target carries no
// times. A time-range target is half-open, [StartMS, EndMS), in elapsed
// playback milliseconds after Opus pre-skip.
//
// "The whole meeting" is its own kind rather than [0, duration): a range that
// happens to span a meeting and a tag on the meeting are different claims.
type AnnotationTarget struct {
	Kind    string `json:"kind"`
	StartMS *int64 `json:"startMs,omitempty"`
	EndMS   *int64 `json:"endMs,omitempty"`
}

// AnnotationActor says who made a mark. ID is the authenticated Nextcloud user,
// never a value a caller supplied. Kind is self-declared by the caller: it is
// attribution for display and undo, not an access control. Readers tolerate
// kinds they do not know (D-743 will add "pipeline").
type AnnotationActor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

const (
	AnnotationsFormatV1 = "cassini.annotations.v1"

	AnnotationTargetMeeting   = "meeting"
	AnnotationTargetTimeRange = "time-range"

	AnnotationActorPerson = "person"
	AnnotationActorAgent  = "agent"

	// Explicit limits, per the design review: start small and inline, and move
	// the document to a chunk set of its own only if growth justifies it. A
	// thousand marks measured at ~52.6 KiB.
	MaxAnnotationTags       = 200
	MaxAnnotationItems      = 2000
	MaxAnnotationLabelRunes = 64
	maxAnnotationActorIDLen = 256
	annotationIDRandomBytes = 16
	annotationIDPrefixTag   = "tag"
	annotationIDPrefixItem  = "mk"
	annotationIDPrefixOp    = "op"
)

// ErrAnnotationsFormatUnsupported means the member is present but in a format
// this reader does not know. Readers treat the recording as carrying no marks
// they can show; they must not treat it as broken.
var ErrAnnotationsFormatUnsupported = errors.New("annotations are in a format this reader does not support")

var (
	annotationIDRE        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	annotationNamespaceRE = regexp.MustCompile(`^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	annotationIDEncoding  = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)
)

// ParseAnnotations decodes a manifest's raw annotations member.
//
// Absent or null answers (nil, nil). A member in another format answers
// ErrAnnotationsFormatUnsupported. Only a v1 member that fails to decode is an
// ordinary error — and even then the recording itself remains readable, because
// DecodePublishedManifest never looks inside this member.
func ParseAnnotations(raw json.RawMessage) (*Annotations, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var probe struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return nil, fmt.Errorf("parse annotations: %w", err)
	}
	if probe.Format != AnnotationsFormatV1 {
		return nil, fmt.Errorf("%w: %q", ErrAnnotationsFormatUnsupported, probe.Format)
	}
	var doc Annotations
	if err := json.Unmarshal(trimmed, &doc); err != nil {
		return nil, fmt.Errorf("parse annotations: %w", err)
	}
	return &doc, nil
}

// Resolved reports whether the document's marks were made against this audio.
func (a *Annotations) Resolved(audioOpusSHA256 string) bool {
	return a != nil && a.AudioOpusSHA256 != "" && a.AudioOpusSHA256 == strings.ToLower(strings.TrimSpace(audioOpusSHA256))
}

// Canonicalize puts the document in its canonical order — tags by label then
// id; meeting targets first, then items by start, end and id — so successive
// revisions diff readably and two writers producing the same marks produce the
// same bytes.
//
// It also makes empty lists empty arrays. The published schema requires tags
// and items to be arrays, and a nil slice encodes as null, so a document whose
// last mark was removed would otherwise fail the schema every reader checks.
func (a *Annotations) Canonicalize() {
	if a == nil {
		return
	}
	if a.Tags == nil {
		a.Tags = []AnnotationTag{}
	}
	if a.Items == nil {
		a.Items = []AnnotationItem{}
	}
	sort.SliceStable(a.Tags, func(i, j int) bool {
		li, lj := strings.ToLower(a.Tags[i].Label), strings.ToLower(a.Tags[j].Label)
		if li != lj {
			return li < lj
		}
		return a.Tags[i].ID < a.Tags[j].ID
	})
	sort.SliceStable(a.Items, func(i, j int) bool {
		ti, tj := a.Items[i].Target, a.Items[j].Target
		mi, mj := ti.Kind == AnnotationTargetMeeting, tj.Kind == AnnotationTargetMeeting
		if mi != mj {
			return mi
		}
		si, sj := int64Value(ti.StartMS), int64Value(tj.StartMS)
		if si != sj {
			return si < sj
		}
		ei, ej := int64Value(ti.EndMS), int64Value(tj.EndMS)
		if ei != ej {
			return ei < ej
		}
		return a.Items[i].ID < a.Items[j].ID
	})
}

// ValidateAnnotations is the writer's check. durationMS is the recording's
// audio.durationMs and bounds every time range.
func ValidateAnnotations(a *Annotations, durationMS int64) error {
	if a == nil {
		return errors.New("annotations: document is missing")
	}
	if a.Format != AnnotationsFormatV1 {
		return fmt.Errorf("annotations.format: want %q, got %q", AnnotationsFormatV1, a.Format)
	}
	if a.Revision < 1 {
		return fmt.Errorf("annotations.revision: must be at least 1, got %d", a.Revision)
	}
	if !sha256HexRE.MatchString(a.AudioOpusSHA256) {
		return errors.New("annotations.audioOpusSha256: expected 64 lowercase hex characters")
	}
	if !annotationNamespaceRE.MatchString(a.TagNamespace) {
		return fmt.Errorf("annotations.tagNamespace: expected urn:uuid:<lowercase uuid>, got %q", a.TagNamespace)
	}
	if len(a.Tags) > MaxAnnotationTags {
		return fmt.Errorf("annotations.tags: %d tags exceeds the limit of %d", len(a.Tags), MaxAnnotationTags)
	}
	if len(a.Items) > MaxAnnotationItems {
		return fmt.Errorf("annotations.items: %d items exceeds the limit of %d", len(a.Items), MaxAnnotationItems)
	}

	tagIDs := make(map[string]bool, len(a.Tags))
	for i, tag := range a.Tags {
		if !annotationIDRE.MatchString(tag.ID) {
			return fmt.Errorf("annotations.tags[%d].id: %q is not a valid id", i, tag.ID)
		}
		if tagIDs[tag.ID] {
			return fmt.Errorf("annotations.tags[%d].id: %q is defined twice", i, tag.ID)
		}
		tagIDs[tag.ID] = true
		if err := ValidateAnnotationLabel(tag.Label); err != nil {
			return fmt.Errorf("annotations.tags[%d].label: %w", i, err)
		}
	}

	itemIDs := make(map[string]bool, len(a.Items))
	for i, item := range a.Items {
		at := fmt.Sprintf("annotations.items[%d]", i)
		if !annotationIDRE.MatchString(item.ID) {
			return fmt.Errorf("%s.id: %q is not a valid id", at, item.ID)
		}
		if itemIDs[item.ID] {
			return fmt.Errorf("%s.id: %q appears twice", at, item.ID)
		}
		itemIDs[item.ID] = true
		if !tagIDs[item.TagID] {
			return fmt.Errorf("%s.tagId: %q names no tag in this document", at, item.TagID)
		}
		if err := validateAnnotationTarget(item.Target, durationMS); err != nil {
			return fmt.Errorf("%s.target: %w", at, err)
		}
		if err := validateAnnotationTime(item.CreatedAtUTC); err != nil {
			return fmt.Errorf("%s.createdAtUtc: %w", at, err)
		}
		if strings.TrimSpace(item.Actor.Kind) == "" {
			return fmt.Errorf("%s.actor.kind: must not be empty", at)
		}
		if id := item.Actor.ID; strings.TrimSpace(id) == "" || len(id) > maxAnnotationActorIDLen {
			return fmt.Errorf("%s.actor.id: must be 1-%d bytes", at, maxAnnotationActorIDLen)
		}
		if !annotationIDRE.MatchString(item.OperationID) {
			return fmt.Errorf("%s.operationId: %q is not a valid id", at, item.OperationID)
		}
	}
	return nil
}

// ValidateAnnotationLabel checks one tag label as a writer must store it:
// already trimmed, 1-64 characters, no control characters.
func ValidateAnnotationLabel(label string) error {
	if label != strings.TrimSpace(label) {
		return errors.New("must not start or end with whitespace")
	}
	if !utf8.ValidString(label) {
		return errors.New("must be valid UTF-8")
	}
	n := utf8.RuneCountInString(label)
	if n == 0 || n > MaxAnnotationLabelRunes {
		return fmt.Errorf("must be 1-%d characters, got %d", MaxAnnotationLabelRunes, n)
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return errors.New("must not contain control characters")
		}
	}
	return nil
}

// NewAnnotationTagID, NewAnnotationItemID and NewAnnotationOperationID mint
// ids. All three are random rather than time-ordered on purpose: a tag id is
// reused across recordings, and a time-ordered one would tell a caller when —
// and so that — someone else first used a label on a meeting they cannot read.
func NewAnnotationTagID() (string, error)       { return newAnnotationID(annotationIDPrefixTag) }
func NewAnnotationItemID() (string, error)      { return newAnnotationID(annotationIDPrefixItem) }
func NewAnnotationOperationID() (string, error) { return newAnnotationID(annotationIDPrefixOp) }

func newAnnotationID(prefix string) (string, error) {
	buf := make([]byte, annotationIDRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("mint annotation id: %w", err)
	}
	return prefix + "_" + annotationIDEncoding.EncodeToString(buf), nil
}

func validateAnnotationTarget(target AnnotationTarget, durationMS int64) error {
	switch target.Kind {
	case AnnotationTargetMeeting:
		if target.StartMS != nil || target.EndMS != nil {
			return errors.New("a meeting target carries no startMs or endMs")
		}
		return nil
	case AnnotationTargetTimeRange:
		if target.StartMS == nil || target.EndMS == nil {
			return errors.New("a time-range target needs both startMs and endMs")
		}
		start, end := *target.StartMS, *target.EndMS
		if start < 0 {
			return fmt.Errorf("startMs %d is negative", start)
		}
		if end <= start {
			return fmt.Errorf("[%d, %d) is empty or reversed", start, end)
		}
		if durationMS <= 0 {
			return errors.New("the recording's duration is unknown, so a time range cannot be checked")
		}
		if end > durationMS {
			return fmt.Errorf("endMs %d is past the end of the audio (%d)", end, durationMS)
		}
		return nil
	default:
		return fmt.Errorf("unknown kind %q", target.Kind)
	}
}

func validateAnnotationTime(value string) error {
	if !strings.HasSuffix(value, "Z") {
		return fmt.Errorf("%q is not a UTC time ending in Z", value)
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return fmt.Errorf("%q is not an RFC 3339 time", value)
	}
	return nil
}

func int64Value(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
