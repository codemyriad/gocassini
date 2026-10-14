package cassini

import (
	"fmt"
	"os"
	"strings"

	"gocassini/internal/inspect"
	"gocassini/internal/portable"
	"gocassini/internal/topics"
)

// topicsTranscript is what a topic embed reads from a meeting: the default
// words transcript as a plain word list, and the sha256 of its published body.
// The digest is the embed's idempotency key (docs/proposals/automatic-tagging,
// "Idempotency"): marks rewrite the file's OpusTags but never this body, so a
// confirmed tag never forces a new embed.
type topicsTranscript struct {
	PayloadSHA256 string
	// Language is the language the transcript records, if any. Production
	// builds record none, so the embed detects its own.
	Language string
	Words    []topics.Word
}

// readTopicsTranscript reads the default words transcript of a delivered
// .opus (or its .json transcription), or of a .meeting bundle directory. A
// file is decoded and checked against its payloadRef.sha256. A bundle is
// rebuilt into the exact body pack would publish, with the pack code itself,
// so both paths give the same digest for the same delivered build; the
// operator falls back to the archive whenever a bundle's digest disagrees
// with the delivered one.
func readTopicsTranscript(path string) (topicsTranscript, error) {
	info, err := os.Stat(path)
	if err != nil {
		return topicsTranscript{}, err
	}
	if info.IsDir() {
		return readTopicsTranscriptFromBundle(path)
	}
	meeting, err := inspect.ExtractMeeting(path)
	if err != nil {
		return topicsTranscript{}, err
	}
	extracted := meeting.Transcript
	if extracted.PayloadSHA256 == "" {
		return topicsTranscript{}, fmt.Errorf("%s: the default transcript has no payload digest", path)
	}
	out := topicsTranscript{PayloadSHA256: extracted.PayloadSHA256, Language: extracted.Language}
	for _, w := range extracted.Words {
		out.Words = append(out.Words, topics.Word{Text: w.Text, StartMS: w.StartMS, EndMS: w.EndMS, Speaker: w.Speaker})
	}
	return out, nil
}

func readTopicsTranscriptFromBundle(dir string) (topicsTranscript, error) {
	source, err := loadPortableMeetingSource(dir)
	if err != nil {
		return topicsTranscript{}, err
	}
	// The manifest only feeds provenance and dates, which live outside the
	// transcript body; the body depends on the bundle's transcripts alone.
	inputs, defaultID, err := assembleTranscriptInputs(portable.Manifest{}, source)
	if err != nil {
		return topicsTranscript{}, err
	}
	for _, input := range inputs {
		if input.ID != defaultID || input.Role != "" {
			continue
		}
		body, ok := input.Body.(portable.TranscriptBody)
		if !ok {
			return topicsTranscript{}, fmt.Errorf("default transcript %q is not a words body: %w", input.ID, inspect.ErrNoDefaultTranscript)
		}
		_, ref, err := portable.EncodeTranscriptBody(body, input.ID, input.Role, portable.DefaultPayloadChunkSize)
		if err != nil {
			return topicsTranscript{}, err
		}
		out := topicsTranscript{PayloadSHA256: ref.SHA256, Language: body.Language}
		for _, item := range body.Items {
			if strings.TrimSpace(item.Text) == "" {
				continue
			}
			out.Words = append(out.Words, topics.Word{Text: item.Text, StartMS: item.StartMS, EndMS: item.EndMS, Speaker: item.Speaker})
		}
		return out, nil
	}
	return topicsTranscript{}, fmt.Errorf("bundle %s has %w", dir, inspect.ErrNoDefaultTranscript)
}
