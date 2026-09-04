package operator

import (
	"os"
	"strings"
	"testing"
)

// The recorder authenticates to Talk with the secret the operator resolved,
// which since D-447 may be one the operator generated itself: the child must
// receive it even though the operator's own environment never carried it.
func TestRecordChildEnvCarriesTheResolvedTalkSecret(t *testing.T) {
	t.Setenv(talkRecordingSecretEnv, "") // registers the restore
	os.Unsetenv(talkRecordingSecretEnv)
	rt := &Runtime{cfg: Config{TalkSharedSecret: "generated-secret"}}
	if got := lookupEnv(rt.recordChildEnv(), talkRecordingSecretEnv); got != "generated-secret" {
		t.Fatalf("recorder env %s = %q, want the resolved secret", talkRecordingSecretEnv, got)
	}
}

// Recording needs all three independently-owned policies at once: STT and LLM
// settings from the operator, plus the Talk secret resolved during startup.
// This guards the composition point where those branches were merged.
func TestRecordChildEnvCombinesSTTLLMAndTalkPolicies(t *testing.T) {
	rt := &Runtime{
		cfg:      Config{TalkSharedSecret: "generated-secret"},
		settings: STTSettings{Quality: sttQualityBest},
		llm: LLMSettings{
			Providers: []LLMProvider{{ID: "local", BaseURL: "http://qwen.internal:8000/v1"}},
			Summary:   LLMStep{Enabled: true, Provider: "local", Model: "qwen"},
		},
	}
	env := rt.recordChildEnv()

	want := map[string]string{
		talkRecordingSecretEnv: "generated-secret",
		envSTTQuality:          sttQualityBest,
		"SUMMARY_BASE_URL":     "http://qwen.internal:8000/v1",
		"SUMMARY_MODEL":        "qwen",
	}
	for key, value := range want {
		if got := lookupEnv(env, key); got != value {
			t.Fatalf("recorder env %s = %q, want %q", key, got, value)
		}
	}
	if got := lookupEnv(env, "SUMMARY_API_KEY"); got != "" {
		t.Fatalf("keyless summary endpoint received API key %q", got)
	}
}

// An explicit secret in the environment is the same value the operator
// resolved; the child sees it exactly once, from the operator.
func TestRecordChildEnvKeepsOneCopyOfAnExplicitSecret(t *testing.T) {
	t.Setenv(talkRecordingSecretEnv, "explicit-secret")
	rt := &Runtime{cfg: Config{TalkSharedSecret: "explicit-secret"}}
	env := rt.recordChildEnv()
	if got := lookupEnv(env, talkRecordingSecretEnv); got != "explicit-secret" {
		t.Fatalf("recorder env %s = %q, want explicit-secret", talkRecordingSecretEnv, got)
	}
	if n := countEnv(env, talkRecordingSecretEnv); n != 1 {
		t.Fatalf("recorder env carries %s %d times, want once", talkRecordingSecretEnv, n)
	}
}

// The resolved secret wins over a stale copy in the environment: the operator
// is the one that registered a secret in Talk, so its value is the one Talk
// will check.
func TestRecordChildEnvReplacesAStaleEnvironmentSecret(t *testing.T) {
	t.Setenv(talkRecordingSecretEnv, "stale-secret")
	rt := &Runtime{cfg: Config{TalkSharedSecret: "resolved-secret"}}
	env := rt.recordChildEnv()
	if got := lookupEnv(env, talkRecordingSecretEnv); got != "resolved-secret" {
		t.Fatalf("recorder env %s = %q, want resolved-secret", talkRecordingSecretEnv, got)
	}
	if n := countEnv(env, talkRecordingSecretEnv); n != 1 {
		t.Fatalf("recorder env carries %s %d times, want once", talkRecordingSecretEnv, n)
	}
}

// A whitespace-only secret is no secret.
func TestRecordChildEnvTreatsWhitespaceAsNoSecret(t *testing.T) {
	t.Setenv(talkRecordingSecretEnv, "") // registers the restore
	os.Unsetenv(talkRecordingSecretEnv)
	rt := &Runtime{cfg: Config{TalkSharedSecret: "   "}}
	if n := countEnv(rt.recordChildEnv(), talkRecordingSecretEnv); n != 0 {
		t.Fatalf("recorder env carries %s %d times, want none", talkRecordingSecretEnv, n)
	}
}

// With no secret at all the environment passes through untouched, so the
// recorder reports the missing secret itself instead of getting an empty one.
func TestRecordChildEnvWithoutASecretAddsNothing(t *testing.T) {
	t.Setenv(talkRecordingSecretEnv, "") // registers the restore
	os.Unsetenv(talkRecordingSecretEnv)
	rt := &Runtime{}
	if n := countEnv(rt.recordChildEnv(), talkRecordingSecretEnv); n != 0 {
		t.Fatalf("recorder env carries %s %d times, want none", talkRecordingSecretEnv, n)
	}
}

func lookupEnv(env []string, key string) string {
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			return strings.TrimPrefix(kv, key+"=")
		}
	}
	return ""
}

func countEnv(env []string, key string) int {
	n := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			n++
		}
	}
	return n
}
