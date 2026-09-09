package insight

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// RenderMarkdown writes the artifact as the document a user keeps: the record
// as YAML frontmatter, then the model's answer.
//
// The provenance is in the file rather than beside it because the file is the
// thing that gets moved, shared and found again a month later, and a document
// that cannot say which meetings, which prompt version and which model produced
// it is a claim with no way to check it.
//
// The frontmatter is written and never read back. Nothing in this repository
// parses YAML, and the machine-readable form of exactly this record is the
// JSON that EncodeRecordJSON writes — so adding a YAML parser would buy a
// second way to read one record and a way for the two to disagree.
func RenderMarkdown(a Artifact) (string, error) {
	if a.Record.Status != StatusSucceeded {
		return "", fmt.Errorf("this insight is %s, so there is no document to write", statusOrUnknown(a.Record.Status))
	}
	if strings.TrimSpace(a.Body) == "" {
		return "", fmt.Errorf("this insight has no body")
	}
	buf := &strings.Builder{}
	writeFrontmatter(buf, a.Record)
	fmt.Fprint(buf, "\n")
	fmt.Fprint(buf, strings.TrimRight(a.Body, "\n")+"\n")
	return buf.String(), nil
}

func statusOrUnknown(status Status) string {
	if status == "" {
		return "in no recorded state"
	}
	return string(status)
}

// EncodeRecordJSON writes the record as the eval harness reads it, so that a
// grader given an .md and a .json is given one run described twice and not two
// descriptions that drifted.
//
// Two-space indentation, as with the context bundle: these files are diffed and
// read by hand.
func EncodeRecordJSON(out io.Writer, record Record) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(record)
}

// writeFrontmatter emits the record as YAML between --- fences.
//
// Every string is emitted double-quoted and escaped, without exception. A
// hand-rolled emitter that decides per value when quoting is needed is a
// generator of subtle bugs — a meeting titled "yes", a room named "12:30", a
// model id starting with an asterisk — and the cost of always quoting is that
// the output is slightly noisier to read. That is the right trade for a file
// nobody parses back.
func writeFrontmatter(buf *strings.Builder, record Record) {
	fmt.Fprint(buf, "---\n")
	fmt.Fprintf(buf, "version: %s\n", yamlString(record.Version))
	fmt.Fprintf(buf, "artifactId: %s\n", yamlString(record.ArtifactID))
	fmt.Fprintf(buf, "status: %s\n", yamlString(string(record.Status)))
	if record.Reason != "" {
		fmt.Fprintf(buf, "reason: %s\n", yamlString(string(record.Reason)))
	}
	if record.Error != "" {
		fmt.Fprintf(buf, "error: %s\n", yamlString(record.Error))
	}
	fmt.Fprintf(buf, "startedAtUtc: %s\n", yamlString(record.StartedAtUTC))
	fmt.Fprintf(buf, "finishedAtUtc: %s\n", yamlString(record.FinishedAtUTC))
	if record.Question != "" {
		fmt.Fprintf(buf, "question: %s\n", yamlString(record.Question))
	}

	fmt.Fprint(buf, "workflow:\n")
	fmt.Fprintf(buf, "  id: %s\n", yamlString(record.Workflow.ID))
	fmt.Fprintf(buf, "  version: %s\n", yamlString(record.Workflow.Version))
	fmt.Fprintf(buf, "  sha256: %s\n", yamlString(record.Workflow.SHA256))

	fmt.Fprint(buf, "provider:\n")
	fmt.Fprintf(buf, "  kind: %s\n", yamlString(record.Provider.Kind))
	fmt.Fprintf(buf, "  baseUrl: %s\n", yamlString(record.Provider.BaseURL))
	fmt.Fprintf(buf, "  model: %s\n", yamlString(record.Provider.Model))

	fmt.Fprint(buf, "context:\n")
	fmt.Fprintf(buf, "  version: %s\n", yamlString(record.Context.Version))
	fmt.Fprintf(buf, "  sha256: %s\n", yamlString(record.Context.SHA256))
	fmt.Fprintf(buf, "  bundles: %d\n", record.Context.Bundles)
	fmt.Fprintf(buf, "  timestamps: %t\n", record.Context.Timestamps)
	fmt.Fprint(buf, "  meetings:\n")
	for _, meeting := range record.Context.Meetings {
		fmt.Fprintf(buf, "    - id: %s\n", yamlString(meeting.ID))
		if meeting.Title != "" {
			fmt.Fprintf(buf, "      title: %s\n", yamlString(meeting.Title))
		}
		if meeting.RoomID != "" {
			fmt.Fprintf(buf, "      roomId: %s\n", yamlString(meeting.RoomID))
		}
		if meeting.RoomName != "" {
			fmt.Fprintf(buf, "      roomName: %s\n", yamlString(meeting.RoomName))
		}
	}
	fmt.Fprint(buf, "---\n")
}

// yamlString renders a value as a YAML double-quoted scalar.
//
// YAML's double-quoted style uses the same escapes JSON does for everything
// this record can contain, so encoding/json produces a valid one — and gets the
// control characters and the non-ASCII cases right, which is where a hand-rolled
// quoter goes wrong.
func yamlString(s string) string {
	encoded, err := json.Marshal(s)
	if err != nil {
		// json.Marshal of a string fails only on invalid UTF-8, which it
		// replaces rather than rejecting; this branch exists so the failure
		// cannot be silent if that ever stops being true.
		return `"?"`
	}
	return string(encoded)
}
