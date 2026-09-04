// Telling the shell that this instance is not the one it looked at (D-616
// followups, QA item 1).
//
// App.svelte reads its setup health ONCE, in onMount, and nothing writes it
// again. Everything that could change that health — building the substrate,
// installing the apps, switching the storage mode — happens down inside
// StoragePanel, which refreshes its own `/storage` view and nothing else. So an
// administrator who fixed their instance on the Setup tab went back to Browse
// and was still told Cassini was not configured.
//
//	App.svelte ──onMount──▶ fetchSetupHealth ──▶ setupNotice ──▶ banner / notice
//	     ▲                                            ▲
//	     │                                            └── nothing wrote this again
//	     └── onSetupChanged ◀── notifySetupChanged() ◀── StoragePanel, after a
//	                                                     successful action
//
// A module-level listener registry rather than a Svelte store, and rather than
// prop-drilling a callback through Setup.svelte: the two ends are three
// components apart and have nothing else to say to each other, so a store would
// be a shared value nobody reads and a prop would be a parameter every
// intermediate component carries without using. This is one function each way,
// it is plain TypeScript, and it can be tested without mounting anything.
//
// It is deliberately NOT a payload. The panel does not know what the shell needs
// to re-read, and the shell does not need to know what the panel did — it goes
// and asks the operator, which is the only thing that can answer.

type Listener = () => void;

const listeners = new Set<Listener>();

// onSetupChanged registers a listener and returns its unsubscribe.
//
// Returning the unsubscribe rather than exposing a remove function is what makes
// the caller's onDestroy a one-liner, and what stops a component that mounts
// twice from leaving a listener behind holding its first instance's state.
export function onSetupChanged(listener: Listener): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

// notifySetupChanged tells every listener that this Nextcloud is no longer the
// one they last looked at.
//
// A listener that throws must not stop the others: they are independent readers
// of the same instance, and one broken surface is a smaller failure than every
// surface staying stale. The error is reported rather than swallowed, because
// the symptom otherwise is the exact papercut this exists to remove.
export function notifySetupChanged(): void {
  for (const listener of [...listeners]) {
    try {
      listener();
    } catch (error) {
      console.error("Cassini: a setup-changed listener failed.", error);
    }
  }
}

// resetSetupListeners drops every listener. Tests only — the registry is
// module-level, so a test that leaves one behind fails a later one.
export function resetSetupListeners(): void {
  listeners.clear();
}
