package transcribe

import (
	"path/filepath"
	"testing"
)

func TestDefaultCacheDirLandsOnThePersistentVolumeUnderAppAPI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	persistent := filepath.Join(home, "volume", "models")
	for _, tc := range []struct {
		name, persist, cacheRoot, want string
	}{
		{"AppAPI, root unset", "/volume", "", "/volume/operator/models"},
		{"AppAPI, image default", " /volume ", imageDefaultCacheRoot, "/volume/operator/models"},
		{"AppAPI, explicit root", "/volume", persistent, persistent},
		{"no AppAPI, explicit root", "", persistent, persistent},
		{"no AppAPI, image default", "", imageDefaultCacheRoot, imageDefaultCacheRoot},
		{"no AppAPI, root unset", "", "", filepath.Join(home, ".cache", "cassini")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("APP_PERSISTENT_STORAGE", tc.persist)
			t.Setenv("CASSINI_CACHE_ROOT", tc.cacheRoot)
			if got := DefaultCacheDir(); got != tc.want {
				t.Fatalf("DefaultCacheDir() = %q, want %q", got, tc.want)
			}
		})
	}
}
