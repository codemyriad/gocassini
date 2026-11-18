package operator

import (
	"bytes"
	"encoding/json"
	"log"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The check agrees with a plain statfs of the same path. Nothing here assumes
// what filesystem t.TempDir() or /dev/shm is on; each candidate is compared
// with its own statfs result.
func TestWarnIfEphemeralMatchesStatfs(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-created-yet")
	for _, path := range []string{t.TempDir(), "/dev/shm", "/", missing} {
		var st syscall.Statfs_t
		want := ""
		if syscall.Statfs(path, &st) == nil {
			switch int64(st.Type) {
			case 0x794c7630:
				want = "overlay"
			case 0x01021994:
				want = "tmpfs"
			}
		}
		if got := ephemeralFSType(path); got != want {
			t.Errorf("ephemeralFSType(%q) = %q, want %q", path, got, want)
		}
		var logs bytes.Buffer
		found := warnIfEphemeral(log.New(&logs, "", 0), path)
		if found != (want != "") {
			t.Errorf("warnIfEphemeral(%q) = %v, want %v", path, found, want != "")
		}
		switch want {
		case "":
			if logs.Len() != 0 {
				t.Errorf("%q: warned for a persistent or missing path: %q", path, logs.String())
			}
		case "overlay":
			if !strings.Contains(logs.String(), path+" is on the container layer (overlay): it is lost when the container is recreated") {
				t.Errorf("%q: overlay warning = %q", path, logs.String())
			}
		case "tmpfs":
			if !strings.Contains(logs.String(), path+" is on tmpfs: it is lost at every container stop and host reboot") {
				t.Errorf("%q: tmpfs warning = %q", path, logs.String())
			}
		}
	}
}

// The persistence fact is a plain boolean, so it is safe in a scrubbed bundle.
func TestScrubbedBundleCarriesDataOnEphemeralStorage(t *testing.T) {
	for _, ephemeral := range []bool{false, true} {
		rt := supportTestRuntime(t)
		rt.dataOnEphemeralStorage = ephemeral
		entries := supportTestDownload(t, rt, ExAppConfig{}, true)
		var configuration map[string]any
		if err := json.Unmarshal(entries["configuration.json"], &configuration); err != nil {
			t.Fatalf("configuration.json: %v", err)
		}
		if got, ok := configuration["data_on_ephemeral_storage"].(bool); !ok || got != ephemeral {
			t.Fatalf("data_on_ephemeral_storage = %#v, want %v", configuration["data_on_ephemeral_storage"], ephemeral)
		}
	}
}
