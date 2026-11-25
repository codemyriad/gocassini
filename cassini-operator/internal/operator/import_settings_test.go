package operator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func importLimitEnv(value string) func(string) string {
	return func(key string) string {
		if key == envMaxConcurrentImports {
			return value
		}
		return ""
	}
}

// The value an administrator saved wins over the deploy environment, which
// applies only until one is saved; nothing is written on start, so a deploy
// env set later still applies to a server whose administrator never saved.
func TestImportLimitPrefersTheSavedValueOverTheDeployEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), importSettingsFileName)
	if n, source := loadImportLimit(path, importLimitEnv(""), nil); n != defaultMaxConcurrentImports || source != importLimitSourceDefault {
		t.Fatalf("no file, no env: %d %s", n, source)
	}
	if n, source := loadImportLimit(path, importLimitEnv("5"), nil); n != 5 || source != importLimitSourceDeploy {
		t.Fatalf("no file, env 5: %d %s", n, source)
	}
	if n, source := loadImportLimit(path, importLimitEnv("50"), nil); n != maxConcurrentImportsLimit || source != importLimitSourceDeploy {
		t.Fatalf("env above the limit: %d %s", n, source)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("loading wrote the settings file: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"max_concurrent_imports": 2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, source := loadImportLimit(path, importLimitEnv("5"), nil); n != 2 || source != importLimitSourceAdmin {
		t.Fatalf("saved 2, env 5: %d %s", n, source)
	}
	// A file that cannot be used falls back to the environment, not to a stop.
	if err := os.WriteFile(path, []byte(`{"max_concurrent_imports": 0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, source := loadImportLimit(path, importLimitEnv("5"), nil); n != 5 || source != importLimitSourceDeploy {
		t.Fatalf("unusable file, env 5: %d %s", n, source)
	}
}

func TestImportSettingsHandlerSavesAndAppliesTheLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), importSettingsFileName)
	rt := &Runtime{importSlots: newImportSlotPool(3), importLimitSource: importLimitSourceDeploy, importSettingsPath: path}
	do := func(method, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		rt.importSettingsHandler(w, httptest.NewRequest(method, "/settings/imports", strings.NewReader(body)))
		return w
	}
	var got importSettingsResponse
	w := do(http.MethodGet, "")
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.MaxConcurrentImports != 3 || got.Source != importLimitSourceDeploy || got.Max != maxConcurrentImportsLimit {
		t.Fatalf("GET: %d %s", w.Code, w.Body.String())
	}
	for _, bad := range []string{`{"max_concurrent_imports": 0}`, `{"max_concurrent_imports": 11}`, `{}`, `{"max_concurrent_imports": 2, "other": 1}`} {
		if w := do(http.MethodPut, bad); w.Code != http.StatusBadRequest {
			t.Fatalf("PUT %s: %d", bad, w.Code)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a refused PUT wrote the settings file")
	}
	w = do(http.MethodPut, `{"max_concurrent_imports": 5}`)
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK || got.MaxConcurrentImports != 5 || got.Source != importLimitSourceAdmin {
		t.Fatalf("PUT 5: %d %s", w.Code, w.Body.String())
	}
	if rt.importSlots.limitNow() != 5 {
		t.Fatalf("the running limit is %d, want 5", rt.importSlots.limitNow())
	}
	// What was saved is what the next start uses, whatever the deploy env says.
	if n, source := loadImportLimit(path, importLimitEnv("2"), nil); n != 5 || source != importLimitSourceAdmin {
		t.Fatalf("after a restart: %d %s", n, source)
	}
}

// Raising the limit starts waiting imports at once; lowering it lets running
// ones finish and starts none until fewer than the new limit run. Waiters are
// served oldest first.
func TestImportSlotPoolLimitChangesWhileImportsRun(t *testing.T) {
	p := newImportSlotPool(1)
	ctx := context.Background()
	first, err := p.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	granted := make(chan int, 2)
	releases := make(chan func(), 2)
	for i := 1; i <= 2; i++ {
		i := i
		go func() {
			release, err := p.acquire(ctx)
			if err == nil {
				granted <- i
				releases <- release
			}
		}()
		// Queue them in a known order.
		deadline := time.Now().Add(5 * time.Second)
		for {
			p.mu.Lock()
			n := len(p.waiters)
			p.mu.Unlock()
			if n == i {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("waiter never queued")
			}
			time.Sleep(time.Millisecond)
		}
	}
	p.setLimit(2)
	if got := <-granted; got != 1 {
		t.Fatalf("raising the limit started waiter %d, want the oldest", got)
	}
	select {
	case got := <-granted:
		t.Fatalf("waiter %d started beyond the limit", got)
	case <-time.After(50 * time.Millisecond):
	}
	// Lower to 1 with 2 running: nothing new starts as one finishes.
	p.setLimit(1)
	first()
	select {
	case got := <-granted:
		t.Fatalf("waiter %d started though 1 still runs at a limit of 1", got)
	case <-time.After(50 * time.Millisecond):
	}
	(<-releases)()
	if got := <-granted; got != 2 {
		t.Fatalf("started %d", got)
	}
	(<-releases)()
	if p.inUse() != 0 {
		t.Fatalf("%d slots held after every import finished", p.inUse())
	}
}

// A waiter whose context ends leaves the line, and one granted just as it
// ended gives its slot back.
func TestImportSlotPoolCanceledWaiterHoldsNothing(t *testing.T) {
	p := newImportSlotPool(1)
	release, _ := p.acquire(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := p.acquire(ctx)
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		p.mu.Lock()
		n := len(p.waiters)
		p.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("waiter never queued")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("a canceled waiter got a slot")
	}
	release()
	if p.inUse() != 0 || len(p.waiters) != 0 {
		t.Fatalf("held %d, waiting %d", p.inUse(), len(p.waiters))
	}
}
