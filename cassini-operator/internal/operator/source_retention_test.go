package operator

import (
	"context"
	"encoding/json"
	"testing"
)

func disposalSettings() STTSettings {
	return STTSettings{MeetingFormat: "json", SourceRetention: sourceDeleteAfterProcessing, TranscriptionEnabled: true, ActiveModel: "prepared", ActiveRevision: "r1"}
}

func TestSourceRetentionValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*STTSettings)
		valid  bool
	}{
		{"delete", func(s *STTSettings) {}, true},
		{"retain video and JSON", func(s *STTSettings) {
			s.SourceRetention = sourceStoragePolicy
			s.RetainVideo = true
			s.TranscriptionEnabled = false
		}, true},
		{"audio publication", func(s *STTSettings) { s.MeetingFormat = "opus" }, false},
		{"video", func(s *STTSettings) { s.RetainVideo = true }, false},
		{"disabled", func(s *STTSettings) { s.TranscriptionEnabled = false }, false},
		{"unprepared", func(s *STTSettings) { s.ActiveRevision = "" }, false},
		{"unknown", func(s *STTSettings) { s.SourceRetention = "typo" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := disposalSettings()
			tc.change(&s)
			if err := normalizeSourceRetention(&s); (err == nil) != tc.valid {
				t.Fatalf("validation: %v", err)
			}
		})
	}
	legacy := STTSettings{MeetingFormat: "json"}
	if err := normalizeSourceRetention(&legacy); err != nil || legacy.SourceRetention != sourceStoragePolicy {
		t.Fatalf("legacy changed: %+v %v", legacy, err)
	}
}

func TestAdmissionFreezesProcessingPolicy(t *testing.T) {
	rt, close := newTestRuntime(t)
	defer close()
	rt.setSettings(disposalSettings())
	req := TriggerRequest{Platform: nextcloudTalkProvider, URL: "https://example.test/call/room", GuestName: defaultGuestName, TalkAuthMode: defaultTalkAuthMode, ProcessingPolicy: &recordingProcessingPolicy{MeetingFormat: "opus"}}
	resp, _, err := rt.prepareRecordJob(context.Background(), nextcloudTalkProvider, "{}", req)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.releaseRecordSlot()
	rt.setSettings(STTSettings{MeetingFormat: "opus"})
	job := mustGetJob(t, rt.store, resp.ID)
	policy := processingPolicy(job)
	if policy == nil || policy.MeetingFormat != "json" || !deletesSourceMedia(job) || policy.Transcription.ActiveRevision != "r1" {
		t.Fatalf("policy: %+v", policy)
	}
	if err := rt.enqueueSealJobNonBlocking(job.ID, 1, "unused", "unused", nowUTCString()); err != nil {
		t.Fatal(err)
	}
	var format string
	if err := rt.store.db.QueryRow(`SELECT format FROM meeting_format WHERE job_id=?`, job.ID).Scan(&format); err != nil || format != "json" {
		t.Fatalf("format %s: %v", format, err)
	}
}

func TestRetainedJSONPolicyDoesNotDeleteSource(t *testing.T) {
	raw, _ := json.Marshal(TriggerRequest{ProcessingPolicy: &recordingProcessingPolicy{MeetingFormat: "json", SourceRetention: sourceStoragePolicy}})
	if deletesSourceMedia(Job{RequestJSON: string(raw)}) || deletesSourceMedia(Job{RequestJSON: "{}"}) {
		t.Fatal("retained or legacy meeting opted into deletion")
	}
}
