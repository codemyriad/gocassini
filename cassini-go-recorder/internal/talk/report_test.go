package talk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gocassini/internal/config"
	coreremux "gocassini/pkg/core/remux"
)

func TestBuildArtifactRemuxReportNil(t *testing.T) {
	report := buildArtifactRemuxReport(nil)
	used, ok := report["used"].(bool)
	if !ok || used {
		t.Fatalf("expected used=false report, got=%v", report)
	}
}

func TestWriteReportIncludesArtifactRemuxPlans(t *testing.T) {
	tmp := t.TempDir()
	rec := &Recorder{
		cfg: config.Config{
			OutputPath: filepath.Join(tmp, "archive.csr"),
			CallURL:    "https://example.test/call/room",
			GuestName:  "recorder-bot",
			Duration:   10 * time.Second,
		},
		baseURL:         "https://example.test",
		roomToken:       "room",
		finalOutputPath: filepath.Join(tmp, "final.mkv"),
		segmentsDir:     filepath.Join(tmp, "segments"),
		startedAt:       time.Unix(1_700_000_000, 0).UTC(),
		artifactRemux: &coreremux.BuildResult{
			SessionJSONPath: filepath.Join(tmp, "sessions", "session.json"),
			OutputPath:      filepath.Join(tmp, "final.mkv"),
			WorkDir:         filepath.Join(tmp, "segments", "artifact-remux-work"),
			Segments:        2,
			StreamPlans: []coreremux.StreamPlan{
				{
					StreamID:         "s_000001",
					Kind:             "video",
					Codec:            "video/vp8",
					TimelineAdjustNS: -15000000,
					OffsetSeconds:    1.5,
					TimelineSamples:  1200,
				},
				{
					StreamID:         "s_000002",
					Kind:             "audio",
					Codec:            "audio/opus",
					TimelineAdjustNS: 0,
					OffsetSeconds:    0.0,
					TimelineSamples:  300,
				},
			},
		},
	}
	if err := os.MkdirAll(rec.segmentsDir, 0o755); err != nil {
		t.Fatalf("mkdir segments: %v", err)
	}

	reportPath := filepath.Join(tmp, "report.json")
	if err := rec.writeReport(reportPath, nil, true, "", false, nil, nil); err != nil {
		t.Fatalf("write report: %v", err)
	}

	raw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	var report map[string]any
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("parse report json: %v", err)
	}

	artifactRemux := asMap(report["artifact_remux"])
	if used, ok := artifactRemux["used"].(bool); !ok || !used {
		t.Fatalf("expected artifact_remux.used=true, got=%v", artifactRemux["used"])
	}
	if segments, ok := artifactRemux["segments"].(float64); !ok || int(segments) != 2 {
		t.Fatalf("expected artifact_remux.segments=2, got=%v", artifactRemux["segments"])
	}
	if adjustedStreams, ok := artifactRemux["adjusted_streams"].(float64); !ok || int(adjustedStreams) != 1 {
		t.Fatalf("expected adjusted_streams=1, got=%v", artifactRemux["adjusted_streams"])
	}
	plans, ok := artifactRemux["stream_plans"].([]any)
	if !ok || len(plans) != 2 {
		t.Fatalf("expected two stream plans, got=%v", artifactRemux["stream_plans"])
	}
	first := asMap(plans[0])
	if first["stream_id"] != "s_000001" {
		t.Fatalf("unexpected first stream plan id: %v", first["stream_id"])
	}
	if first["timeline_adjust_ns"] != float64(-15000000) {
		t.Fatalf("unexpected first stream adjustment: %v", first["timeline_adjust_ns"])
	}
}

func TestDeriveFinalOutputPath(t *testing.T) {
	cases := []struct {
		name       string
		archive    string
		final      string
		wantOutput string
	}{
		{
			name:       "explicit final output wins",
			archive:    "/tmp/meeting.csr",
			final:      "/tmp/custom.mkv",
			wantOutput: "/tmp/custom.mkv",
		},
		{
			name:       "csr archive derives mkv",
			archive:    "/tmp/meeting.csr",
			wantOutput: "/tmp/meeting.mkv",
		},
		{
			name:       "mkv output stays mkv",
			archive:    "/tmp/meeting.mkv",
			wantOutput: "/tmp/meeting.mkv",
		},
		{
			name:       "extensionless output appends mkv",
			archive:    "/tmp/meeting",
			wantOutput: "/tmp/meeting.mkv",
		},
		{
			name:       "uppercase csr derives mkv",
			archive:    "/tmp/MEETING.CSR",
			wantOutput: "/tmp/MEETING.mkv",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveFinalOutputPath(tc.archive, tc.final)
			if got != tc.wantOutput {
				t.Fatalf("deriveFinalOutputPath(%q, %q) = %q, want %q", tc.archive, tc.final, got, tc.wantOutput)
			}
		})
	}
}
