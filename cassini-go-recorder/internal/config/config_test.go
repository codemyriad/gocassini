package config

import "testing"

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
