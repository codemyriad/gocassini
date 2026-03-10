package config

import (
	"testing"
	"time"
)

func TestFromFlagsTalkModeRequiresCallURL(t *testing.T) {
	_, err := FromFlags([]string{
		"--mode", "talk",
		"--output", "/tmp/out.csr",
	})
	if err == nil {
		t.Fatalf("expected error when --call-url is missing in talk mode")
	}
}

func TestFromFlagsSimulateModeAllowsEmptyCallURL(t *testing.T) {
	cfg, err := FromFlags([]string{
		"--mode", "simulate",
		"--output", "/tmp/out.csr",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.CallURL != "" {
		t.Fatalf("expected empty call-url in simulate mode, got %q", cfg.CallURL)
	}
}

func TestFromFlagsTalkModeAcceptsCallURL(t *testing.T) {
	cfg, err := FromFlags([]string{
		"--mode", "talk",
		"--call-url", "https://cloud.example.com/call/roomtoken",
		"--output", "/tmp/out.csr",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.CallURL != "https://cloud.example.com/call/roomtoken" {
		t.Fatalf("unexpected call-url: %q", cfg.CallURL)
	}
}

func TestFromFlagsTalkModeAcceptsMKVOutputPath(t *testing.T) {
	cfg, err := FromFlags([]string{
		"--mode", "talk",
		"--call-url", "https://cloud.example.com/call/roomtoken",
		"--output", "/tmp/out.mkv",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OutputPath != "/tmp/out.mkv" {
		t.Fatalf("unexpected output path: %q", cfg.OutputPath)
	}
}

func TestFromFlagsTalkDefaultsAutoStopOnEmptyRoom(t *testing.T) {
	cfg, err := FromFlags([]string{
		"--mode", "talk",
		"--call-url", "https://cloud.example.com/call/roomtoken",
		"--output", "/tmp/out.csr",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Duration != 0 {
		t.Fatalf("expected default duration 0 (disabled), got %s", cfg.Duration)
	}
	if !cfg.StopWhenRoomEmpty {
		t.Fatalf("expected stop-when-room-empty to default to true")
	}
	if cfg.RoomEmptyGrace != 30*time.Second {
		t.Fatalf("expected room-empty-grace=30s, got %s", cfg.RoomEmptyGrace)
	}
}

func TestFromFlagsRejectsNegativeDuration(t *testing.T) {
	_, err := FromFlags([]string{
		"--mode", "talk",
		"--call-url", "https://cloud.example.com/call/roomtoken",
		"--output", "/tmp/out.csr",
		"--duration", "-1",
	})
	if err == nil {
		t.Fatalf("expected error for negative duration")
	}
}

func TestFromFlagsRejectsNegativeRoomEmptyGrace(t *testing.T) {
	_, err := FromFlags([]string{
		"--mode", "talk",
		"--call-url", "https://cloud.example.com/call/roomtoken",
		"--output", "/tmp/out.csr",
		"--room-empty-grace", "-0.5",
	})
	if err == nil {
		t.Fatalf("expected error for negative room-empty-grace")
	}
}

func TestFromFlagsReportIsDisabledByDefault(t *testing.T) {
	cfg, err := FromFlags([]string{
		"--mode", "talk",
		"--call-url", "https://cloud.example.com/call/roomtoken",
		"--output", "/tmp/out.csr",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.WriteReport {
		t.Fatalf("expected write-report to default to false")
	}
}

func TestFromFlagsAcceptsWriteReport(t *testing.T) {
	cfg, err := FromFlags([]string{
		"--mode", "talk",
		"--call-url", "https://cloud.example.com/call/roomtoken",
		"--output", "/tmp/out.csr",
		"--write-report",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.WriteReport {
		t.Fatalf("expected write-report to be true")
	}
}
