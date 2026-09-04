// Reloading the page after a setup action, and carrying its result across
// (D-616 followups, QA item 1).
//
// The shell reads its setup health ONCE, in App.svelte's onMount, and never
// again. Everything that could change that health — building the substrate,
// installing the apps, switching the storage mode — happens inside StoragePanel,
// which refreshes its own `/storage` view and nothing else. So an administrator
// who fixed their instance on the Setup tab went back to Browse and was still
// told Cassini was not configured, until they refreshed the browser by hand.
//
//	onMount ──▶ fetchSetupHealth ──▶ setupNotice ──▶ banner / blocking notice
//	                                      ▲
//	                                      └── nothing ever writes this again
//
// The fix is the refresh they were doing anyway. It is the right shape here
// rather than a lazy one: the app is a web component on Nextcloud's own page
// (not an iframe), the active surface lives in `window.location.hash`, and the
// viewer's theme is in localStorage — so a reload lands back on the Setup tab
// with nothing lost. Making `setupNotice` a refreshable store instead would fix
// this one reader and leave every other thing the page cached at mount.
//
// The one thing a reload WOULD lose is the result the administrator just earned
// ("5 recordings were copied into …"), which is why it is handed across in
// sessionStorage and rendered once on the other side.

// storageFlashKey is deliberately specific. sessionStorage is shared with
// Nextcloud's own page and every other app on it.
const storageFlashKey = "cassini.storage.flash";

// StorageFlash is one sentence to show after the reload, plus how to show it.
export interface StorageFlash {
  tone: "success" | "warning";
  message: string;
  detail?: string;
}

function session(): Storage | null {
  try {
    return globalThis.sessionStorage ?? null;
  } catch {
    // Storage can throw on access, not only on use — a browser with cookies
    // fully blocked does exactly that. A missing flash is a cosmetic loss.
    return null;
  }
}

// writeStorageFlash stashes a message for the page that is about to load.
export function writeStorageFlash(flash: StorageFlash): void {
  try {
    session()?.setItem(storageFlashKey, JSON.stringify(flash));
  } catch {
    // Quota, or a private-mode Storage that accepts the object and refuses the
    // write. The reload still happens; only the sentence is lost.
  }
}

// readStorageFlash returns the stashed message and CLEARS it, so a reload of the
// reload does not repeat it. Read-and-clear rather than read-then-clear because
// the two must not come apart if the render throws in between.
export function readStorageFlash(): StorageFlash | null {
  const store = session();
  if (!store) {
    return null;
  }
  let raw: string | null = null;
  try {
    raw = store.getItem(storageFlashKey);
    store.removeItem(storageFlashKey);
  } catch {
    return null;
  }
  if (!raw) {
    return null;
  }
  try {
    const parsed = JSON.parse(raw) as Partial<StorageFlash>;
    if (typeof parsed?.message !== "string" || parsed.message === "") {
      return null;
    }
    return {
      tone: parsed.tone === "warning" ? "warning" : "success",
      message: parsed.message,
      detail: typeof parsed.detail === "string" ? parsed.detail : undefined,
    };
  } catch {
    return null;
  }
}

// reloadPage reloads the whole Nextcloud page.
//
// Injectable, because jsdom's `location.reload` is not callable and a test that
// could not observe this would leave the entire fix unpinned.
export let reloadPage: () => void = () => {
  globalThis.location?.reload();
};

// setReloadPage replaces the reloader. Tests only.
export function setReloadPage(next: () => void): void {
  reloadPage = next;
}
