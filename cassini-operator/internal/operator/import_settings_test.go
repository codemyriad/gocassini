package operator

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Without a saved value the default applies; a saved value is what every
// start uses. Nothing is written on start.
func TestImportLimitIsTheSavedValueOrTheDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), importSettingsFileName)
	if n := loadImportLimit(path, nil); n != defaultMaxConcurrentImports {
		t.Fatalf("no file: %d", n)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("loading wrote the settings file: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"max_concurrent_imports": 7}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if n := loadImportLimit(path, nil); n != 7 {
		t.Fatalf("saved 7: %d", n)
	}
}

// A saved file that cannot be used is kept as a .rejected-N copy, like the
// other settings files, and the default runs until an administrator saves.
func TestAnUnusableImportSettingsFileIsKeptAndPassedOver(t *testing.T) {
	path := filepath.Join(t.TempDir(), importSettingsFileName)
	if err := os.WriteFile(path, []byte(`{"max_concurrent_imports": 50}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var logged strings.Builder
	if n := loadImportLimit(path, log.New(&logged, "", 0)); n != defaultMaxConcurrentImports {
		t.Fatalf("unusable file: %d", n)
	}
	kept, _ := filepath.Glob(path + ".rejected-*")
	if len(kept) != 1 {
		t.Fatalf("rejected copies: %v", kept)
	}
	if raw, _ := os.ReadFile(kept[0]); string(raw) != `{"max_concurrent_imports": 50}` {
		t.Fatalf("the kept copy holds %q", raw)
	}
	if !strings.Contains(logged.String(), filepath.Base(kept[0])) {
		t.Fatalf("the log does not name the kept copy: %s", logged.String())
	}
}

func TestImportSettingsHandlerSavesAndAppliesTheLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), importSettingsFileName)
	rt := &Runtime{importSlots: newImportSlotPool(defaultMaxConcurrentImports), importsServed: true, importSettingsPath: path}
	do := func(method, body string) (*httptest.ResponseRecorder, importSettingsResponse) {
		t.Helper()
		w := httptest.NewRecorder()
		rt.importSettingsHandler(w, httptest.NewRequest(method, "/settings/imports", strings.NewReader(body)))
		var got importSettingsResponse
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		return w, got
	}
	want := func(name string, w *httptest.ResponseRecorder, got importSettingsResponse, n int) {
		t.Helper()
		if w.Code != http.StatusOK || got.MaxConcurrentImports != n || got.Default != defaultMaxConcurrentImports || got.Max != maxConcurrentImportsLimit {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
		if rt.importSlots.limitNow() != n {
			t.Fatalf("%s: the running limit is %d, want %d", name, rt.importSlots.limitNow(), n)
		}
	}
	w, got := do(http.MethodGet, "")
	want("GET", w, got, defaultMaxConcurrentImports)
	for _, bad := range []string{`{"max_concurrent_imports": 0}`, `{"max_concurrent_imports": 11}`, `{}`, `{"max_concurrent_imports": 2, "other": 1}`} {
		if w, _ := do(http.MethodPut, bad); w.Code != http.StatusBadRequest {
			t.Fatalf("PUT %s: %d", bad, w.Code)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a refused PUT wrote the settings file")
	}
	w, got = do(http.MethodPut, `{"max_concurrent_imports": 7}`)
	want("PUT 7", w, got, 7)
	if n := loadImportLimit(path, nil); n != 7 {
		t.Fatalf("after a restart: %d", n)
	}
	if w, _ := do(http.MethodDelete, ""); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE: %d", w.Code)
	}
}

// Where imports are not served (no AppAPI, no Nextcloud archive, no CLI) the
// setting is not offered: the panel hides on 404, and nothing is saved.
func TestImportSettingsAreNotOfferedWhereImportsAreOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), importSettingsFileName)
	rt := &Runtime{importSlots: newImportSlotPool(defaultMaxConcurrentImports), importSettingsPath: path}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		w := httptest.NewRecorder()
		rt.importSettingsHandler(w, httptest.NewRequest(method, "/settings/imports", strings.NewReader(`{"max_concurrent_imports": 5}`)))
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s: %d %s", method, w.Code, w.Body.String())
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a server without imports saved the setting")
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

// Through Nextcloud the operator is reached only on routes info.xml declares
// for that verb; the setting is ADMIN.
func TestImportSettingsRoutesAreDeclaredForAdministrators(t *testing.T) {
	routes := manifestRoutes(t)
	for _, verb := range []string{"GET", "PUT"} {
		if got := manifestAccess(routes, "operator/settings/imports", verb); got != "ADMIN" {
			t.Errorf("%s operator/settings/imports = %q, want ADMIN", verb, got)
		}
	}
}
