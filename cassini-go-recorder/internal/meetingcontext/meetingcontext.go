// Package meetingcontext defines cassini.meetings.context.v1: one or more
// meetings rendered as context an agent can read.
//
// It is its own package because the contract has more than one consumer — the
// `cassini meetings context` command, the published read surface, and the
// insight run seam — and a contract only one package can name is one every
// other consumer re-declares. Each re-declaration is a second implementation
// of a published format, which is precisely the drift the version string
// exists to make visible (D-715).
//
// Nothing here opens a file, shells out to ffprobe or touches the network. The
// producer does that and hands Build the result, so a consumer can decode,
// render and test a bundle with no media tooling installed at all.
package meetingcontext

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Version identifies the context bundle's shape. Bumping it is a breaking
// change for every consumer, so it is versioned from the start.
const Version = "cassini.meetings.context.v1"

// TranscriptSourceDerived is the only transcript provenance this format
// carries today, and it is load-bearing rather than decoration.
//
// An operator-published .opus — the file a user opens through Nextcloud —
// holds raw-ASR words and nothing else: no readable transcript, no display
// transcript. So the prose in every bundle is assembled from word timings, and
// a consumer must not present it as editorially cleaned-up text.
const TranscriptSourceDerived = "derived-from-words"

// MeetingContext is one meeting rendered as agent-readable context: what the
// meeting was, who spoke, the transcript as prose, and the summary if one was
// generated.
type MeetingContext struct {
	Version  string    `json:"version"`
	Meeting  Meeting   `json:"meeting"`
	Speakers []Speaker `json:"speakers,omitempty"`

	// TranscriptSource says how the prose was produced. Always
	// TranscriptSourceDerived today, and deliberately explicit: see the
	// constant for why a consumer must not read it as an edited transcript.
	TranscriptSource string `json:"transcriptSource"`
	// SourceTranscriptID and SourceTranscriptFormat identify the transcript the
	// prose was derived from, so a claim can be traced back to the file.
	SourceTranscriptID     string `json:"sourceTranscriptId,omitempty"`
	SourceTranscriptFormat string `json:"sourceTranscriptFormat,omitempty"`
	Language               string `json:"language,omitempty"`
	WordCount              int    `json:"wordCount"`

	Segments []Segment `json:"segments"`
	Summary  Summary   `json:"summary"`
}

// Meeting is what the meeting was.
type Meeting struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	CreatedAtUTC string `json:"createdAtUtc,omitempty"`
	DurationMS   int64  `json:"durationMs,omitempty"`
	// RoomID and RoomName say which conversation this meeting came out of
	// (D-640). Both optional — a meeting with no room is an ordinary state —
	// and both are read from the catalog entry rather than from the file,
	// because the room's current name lives only in the catalog.
	//
	// They are here because "which room was this?" is a question an agent
	// summarising a meeting routinely needs answered, and the command the docs
	// point it at for reading a meeting used to hand back a document that could
	// not answer it.
	RoomID   string `json:"roomId,omitempty"`
	RoomName string `json:"roomName,omitempty"`
}

// Speaker is one member of the meeting's roster.
type Speaker struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Segment is one speaker-attributed paragraph of derived prose.
type Segment struct {
	Speaker      string `json:"speaker,omitempty"`
	SpeakerLabel string `json:"speakerLabel,omitempty"`
	StartMS      int64  `json:"startMs"`
	EndMS        int64  `json:"endMs"`
	Text         string `json:"text"`
}

// Summary carries the generated summary. Present is explicit so a consumer can
// tell "no summary was generated" from "the summary was empty" — summaries are
// LLM-gated, and a deployment with no summariser configured publishes
// transcripts without them.
type Summary struct {
	Present  bool   `json:"present"`
	Format   string `json:"format,omitempty"`
	Model    string `json:"model,omitempty"`
	Markdown string `json:"markdown,omitempty"`
}

// Bundle is the document cassini.meetings.context.v1 describes: one or more
// meetings, in the order the caller asked for them.
//
// One meeting and many are the same document type on purpose. Asking a
// question of three meetings is asking it of one more times, so a consumer
// that can read a bundle can read either, and the multi-meeting case never
// became a second format.
type Bundle struct {
	Meetings []MeetingContext
}

// multiDocument is the wire shape of a bundle carrying more than one meeting.
//
// A one-meeting bundle is written as the bare meeting object instead: that is
// the v1 document consumers already read, and changing it to gain a wrapper
// would break every one of them for nothing. So the many-meeting form is
// strictly additive — a "meetings" array whose every element is itself a
// complete, valid v1 meeting document, its own version string included. A v1
// reader handed one finds the version it knows and finds documents it knows
// inside, rather than a familiar-looking shape that silently describes only
// the first meeting.
type multiDocument struct {
	Version  string           `json:"version"`
	Meetings []MeetingContext `json:"meetings"`
}

// MarshalJSON writes the one-meeting or many-meeting form, whichever the
// bundle holds.
func (b Bundle) MarshalJSON() ([]byte, error) {
	if len(b.Meetings) == 0 {
		return nil, errors.New("a " + Version + " bundle must carry at least one meeting")
	}
	if len(b.Meetings) == 1 {
		return json.Marshal(b.Meetings[0])
	}
	return json.Marshal(multiDocument{Version: Version, Meetings: b.Meetings})
}

// The two keys that decide which form a document is. The schema puts
// `additionalProperties: false` on both branches of its oneOf, so a key from
// the other form is not a stray field to ignore — it means the document is
// neither form.
const (
	meetingKey  = "meeting"
	meetingsKey = "meetings"
)

// UnmarshalJSON accepts either form, and says nothing about the version — that
// is DecodeJSON's job, so that a caller who reaches for encoding/json directly
// still gets the right shape while the version gate stays somewhere a reader
// can see it.
//
// Which form a document is, is decided by the keys it carries rather than by
// which unmarshal happens to succeed. encoding/json ignores unknown fields, so
// "try the container, fall back to the single meeting" reads a document that is
// both forms at once as a container and drops its top-level meeting on the
// floor, and reads a container nested inside a container as a meeting with no
// id, no speech and no summary. Both are documents the published schema
// rejects, and answering a question out of either is worse than refusing them.
func (b *Bundle) UnmarshalJSON(data []byte) error {
	fields, err := documentFields(data)
	if err != nil {
		return err
	}
	_, single := fields[meetingKey]
	container, many := fields[meetingsKey]
	switch {
	case single && many:
		return fmt.Errorf("the document carries both %q and %q, so it is neither one meeting nor a container of meetings", meetingKey, meetingsKey)
	case many:
		return b.unmarshalContainer(container)
	case single:
		var one MeetingContext
		if err := json.Unmarshal(data, &one); err != nil {
			return err
		}
		b.Meetings = []MeetingContext{one}
		return nil
	default:
		return fmt.Errorf("the document carries neither %q nor %q, so there is nothing in it to read", meetingKey, meetingsKey)
	}
}

func (b *Bundle) unmarshalContainer(container json.RawMessage) error {
	var raws []json.RawMessage
	if err := json.Unmarshal(container, &raws); err != nil {
		return err
	}
	meetings := make([]MeetingContext, 0, len(raws))
	for i, raw := range raws {
		fields, err := documentFields(raw)
		if err != nil {
			return err
		}
		if _, nested := fields[meetingsKey]; nested {
			return fmt.Errorf("meeting %d of %d is itself a container of meetings; every element of %q is one complete meeting, never another list",
				i+1, len(raws), meetingsKey)
		}
		var one MeetingContext
		if err := json.Unmarshal(raw, &one); err != nil {
			return err
		}
		meetings = append(meetings, one)
	}
	b.Meetings = meetings
	return nil
}

// documentFields reads a document's top-level keys without committing to a
// shape, so the form can be decided before anything is decoded into one.
func documentFields(data []byte) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

// EncodeJSON writes the bundle as the JSON `cassini meetings context --json`
// publishes.
//
// The two-space indentation is part of the published bytes rather than a
// formatting preference: this output is diffed, pasted and pinned by hand.
func EncodeJSON(out io.Writer, b Bundle) error {
	if len(b.Meetings) == 0 {
		return fmt.Errorf("write %s: a bundle must carry at least one meeting", Version)
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(b)
}

// DecodeJSON reads a cassini.meetings.context.v1 document in either form.
//
// It refuses a version it does not recognise instead of decoding what it can.
// A consumer that silently accepts a later version reads a shape it does not
// understand and then answers confidently from it, which is worse than not
// answering.
func DecodeJSON(data []byte) (Bundle, error) {
	var envelope struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return Bundle{}, fmt.Errorf("parse %s: %w", Version, err)
	}
	if envelope.Version != Version {
		return Bundle{}, fmt.Errorf("parse %s: the document declares version %q, which this reader does not understand", Version, envelope.Version)
	}

	var bundle Bundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return Bundle{}, fmt.Errorf("parse %s: %w", Version, err)
	}
	if len(bundle.Meetings) == 0 {
		return Bundle{}, fmt.Errorf("parse %s: the document carries no meetings", Version)
	}
	for i, meeting := range bundle.Meetings {
		if meeting.Version != Version {
			return Bundle{}, fmt.Errorf("parse %s: meeting %d of %d declares version %q, which this reader does not understand",
				Version, i+1, len(bundle.Meetings), meeting.Version)
		}
	}
	return bundle, nil
}

// BuildInput is everything a bundle needs about one meeting, in a shape that
// names no CLI type and no media-reading type.
//
// It is deliberately not inspect.ExtractedMeeting: that type is the product of
// shelling out to ffprobe, and a consumer that only decodes and renders
// bundles must not inherit a media-tooling dependency to do it. The producer
// extracts, and fills this in.
type BuildInput struct {
	// ID is the id the caller asked for. It wins over the manifest's own
	// meeting id so the bundle can be correlated with the catalog entry it came
	// from: the manifest id is content-derived (a hash of the audio) and is not
	// the id a published catalog indexes by.
	ID           string
	Title        string
	CreatedAtUTC string
	DurationMS   int64
	RoomID       string
	RoomName     string

	Speakers []Speaker

	SourceTranscriptID     string
	SourceTranscriptFormat string
	Language               string
	WordCount              int

	Segments []Segment
	Summary  Summary
}

// Build assembles one meeting's context.
//
// It is the only way to get a MeetingContext with Version and TranscriptSource
// set, which is the point of having it: both are claims the document makes
// about itself rather than data in it, and a producer able to forget either
// would publish a bundle that lies about what it is.
func Build(in BuildInput) MeetingContext {
	bundle := MeetingContext{
		Version: Version,
		Meeting: Meeting{
			ID:           strings.TrimSpace(in.ID),
			Title:        strings.TrimSpace(in.Title),
			CreatedAtUTC: strings.TrimSpace(in.CreatedAtUTC),
			DurationMS:   in.DurationMS,
			RoomID:       strings.TrimSpace(in.RoomID),
			RoomName:     strings.TrimSpace(in.RoomName),
		},
		TranscriptSource:       TranscriptSourceDerived,
		SourceTranscriptID:     in.SourceTranscriptID,
		SourceTranscriptFormat: in.SourceTranscriptFormat,
		Language:               in.Language,
		WordCount:              in.WordCount,
		// Never nil: "segments": null and "segments": [] read differently to a
		// consumer, and a meeting with no transcribed speech is the second one.
		Segments: make([]Segment, 0, len(in.Segments)),
		Summary:  in.Summary,
	}
	bundle.Speakers = append(bundle.Speakers, in.Speakers...)
	bundle.Segments = append(bundle.Segments, in.Segments...)
	return bundle
}
