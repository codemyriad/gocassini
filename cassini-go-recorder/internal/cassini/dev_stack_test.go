package cassini

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func testEnv(values map[string]string) envLookupFunc {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestResolveDevStackPlanDefaultsPreserveCompatibility(t *testing.T) {
	plan, rest, err := resolveDevStackPlan("plan", nil, testEnv(nil))
	if err != nil {
		t.Fatalf("resolveDevStackPlan: %v", err)
	}
	if len(rest) != 0 {
		t.Fatalf("unexpected rest args: %v", rest)
	}
	if plan.PublicMode != devStackPublicLocalHTTP {
		t.Fatalf("PublicMode = %q", plan.PublicMode)
	}
	if plan.ServiceMode != devStackServiceLegacyDefault {
		t.Fatalf("ServiceMode = %q", plan.ServiceMode)
	}
	if plan.SpreedProfile != "full" {
		t.Fatalf("SpreedProfile = %q, want full", plan.SpreedProfile)
	}
	if plan.CassiniMode != devStackCassiniNone {
		t.Fatalf("CassiniMode = %q", plan.CassiniMode)
	}
	if plan.PatchMode != devStackPatchAuto {
		t.Fatalf("PatchMode = %q", plan.PatchMode)
	}
}

func TestResolveDevStackPlanRejectsRemoteInputsWithoutRemoteMode(t *testing.T) {
	_, _, err := resolveDevStackPlan("plan", nil, testEnv(map[string]string{
		"CASSINI_HARNESS_PUBLIC_URL": "https://16a.tail.example",
	}))
	if err == nil {
		t.Fatal("expected remote input validation error")
	}
	if !strings.Contains(err.Error(), "remote harness inputs require --public-mode remote-https") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveDevStackPlanRemoteHTTPS(t *testing.T) {
	plan, _, err := resolveDevStackPlan("plan", []string{
		"--public-mode", "remote-https",
		"--public-url", "https://16a.tail.example/",
		"--media-host", "100.127.22.64",
		"--services", "full-remote",
	}, testEnv(nil))
	if err != nil {
		t.Fatalf("resolveDevStackPlan: %v", err)
	}
	if plan.PublicURL != "https://16a.tail.example" {
		t.Fatalf("PublicURL = %q", plan.PublicURL)
	}
	if plan.PublicHost != "16a.tail.example" {
		t.Fatalf("PublicHost = %q", plan.PublicHost)
	}
	if plan.SignalingPublicURL != "https://16a.tail.example:8443" {
		t.Fatalf("SignalingPublicURL = %q", plan.SignalingPublicURL)
	}
	if !plan.RemoteConfigRequested {
		t.Fatal("expected remote config to be requested")
	}
}

func TestResolveDevStackPlanInstalledRecordingRequiresInstalledCassini(t *testing.T) {
	_, _, err := resolveDevStackPlan("up", []string{"--recording-backend", "installed-exapp"}, testEnv(nil))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "requires --cassini installed-exapp") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveDevStackPlanInstalledCassiniRejectsCoreServices(t *testing.T) {
	_, _, err := resolveDevStackPlan("up", []string{"--cassini", "installed-exapp", "--services", "core"}, testEnv(nil))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "requires service mode appapi") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveDevStackPlanRecordingBackendRequiresMediaServices(t *testing.T) {
	_, _, err := resolveDevStackPlan("up", []string{"--recording-backend", "direct-operator", "--services", "appapi"}, testEnv(nil))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "requires service mode full") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveDevStackPlanInstalledExAppRecordingShape(t *testing.T) {
	plan, _, err := resolveDevStackPlan("up", []string{"--cassini", "installed-exapp", "--recording-backend", "installed-exapp", "--services", "full", "--build"}, testEnv(nil))
	if err != nil {
		t.Fatalf("resolveDevStackPlan: %v", err)
	}
	if plan.ExAppImageMode != devStackImageBuild {
		t.Fatalf("ExAppImageMode = %q", plan.ExAppImageMode)
	}
}

func TestResolveDevStackPlanScopesLifecycleFlags(t *testing.T) {
	_, _, err := resolveDevStackPlan("down", []string{"--reset"}, testEnv(nil))
	if err == nil || !strings.Contains(err.Error(), "apply only to stack up") {
		t.Fatalf("expected up-only lifecycle error, got %v", err)
	}

	_, _, err = resolveDevStackPlan("up", []string{"--full"}, testEnv(nil))
	if err == nil || !strings.Contains(err.Error(), "apply only to stack stop/down") {
		t.Fatalf("expected stop-only full error, got %v", err)
	}
}

func TestResolveDevStackPlanStopFull(t *testing.T) {
	plan, _, err := resolveDevStackPlan("down", []string{"--full"}, testEnv(nil))
	if err != nil {
		t.Fatalf("resolveDevStackPlan: %v", err)
	}
	if !plan.StopFull {
		t.Fatal("expected StopFull")
	}
}

func TestRunDevStackPlanPrintsResolvedPlan(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runDevStack(context.Background(), ".", []string{"plan", "--patch=none"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runDevStack code=%d stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"public:\n  mode: local-http",
		"cassini:\n  mode: none",
		"patch:\n  mode: none",
		"lifecycle:\n  existing_resources: fail",
		"validation: ok",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("plan output missing %q: %s", want, out)
		}
	}
}

func TestRunDevStackUpPassesResolvedEnv(t *testing.T) {
	prevExec := runDevScriptExec
	defer func() { runDevScriptExec = prevExec }()

	var gotScript string
	var gotEnv []string
	runDevScriptExec = func(_ context.Context, _ string, relativeScript string, _ []string, extraEnv []string, _ io.Writer, _ io.Writer) int {
		gotScript = relativeScript
		gotEnv = extraEnv
		return 0
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runDevStack(context.Background(), ".", []string{"up", "--services", "core"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runDevStack code=%d stderr=%q", code, stderr.String())
	}
	if gotScript != "harness/bin/up.sh" {
		t.Fatalf("script = %q", gotScript)
	}
	joined := strings.Join(gotEnv, "\n")
	if !strings.Contains(joined, "CASSINI_HARNESS_SERVICE_MODE=core") || !strings.Contains(joined, "SPREED_PROFILE=default") {
		t.Fatalf("missing resolved env in %v", gotEnv)
	}
}
