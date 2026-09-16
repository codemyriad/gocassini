// A drawer or sheet opened over the page. Focus moves into it (to its close
// button, as Manage tags does), Tab and Shift+Tab go round inside it rather
// than out of it, and when it closes focus goes back to what opened it. What
// lies under it is made inert by the shell, so this only has to keep the keys
// in; this is the part the shell cannot do from outside.

const FOCUSABLE =
  'a[href], button, input, select, textarea, summary, [tabindex]:not([tabindex="-1"]), [contenteditable="true"]';

// The element that really has focus, through any shadow roots on the way.
function deepActive(scope: Document | ShadowRoot): Element | null {
  let active = scope.activeElement;
  while (active?.shadowRoot?.activeElement) active = active.shadowRoot.activeElement;
  return active;
}

function focusables(node: HTMLElement): HTMLElement[] {
  return [...node.querySelectorAll<HTMLElement>(FOCUSABLE)].filter(
    (element) =>
      !element.matches(":disabled") &&
      element.tabIndex >= 0 &&
      element.getClientRects().length > 0 &&
      !element.closest("[inert]"),
  );
}

export function focusLayer(node: HTMLElement) {
  const scope = node.getRootNode() as Document | ShadowRoot;
  const opener = deepActive(document);

  // After the flush that opened it, once the page under it has gone inert and
  // let go of the focus. A timer, not a frame: a frame never comes in a tab
  // the browser is not painting.
  const timer = setTimeout(() => {
    if (node.contains(scope.activeElement)) return;
    const start = node.querySelector<HTMLElement>(".close-button, [data-close]") ?? focusables(node)[0];
    (start ?? node).focus({ preventScroll: true });
  }, 0);

  function onKeydown(event: KeyboardEvent) {
    if (event.key !== "Tab" || event.defaultPrevented) return;
    const items = focusables(node);
    if (items.length === 0) return;
    const [edge, next] = event.shiftKey ? [items[0], items.at(-1)] : [items.at(-1), items[0]];
    if (scope.activeElement === edge) {
      event.preventDefault();
      next?.focus();
    }
  }
  node.addEventListener("keydown", onKeydown);

  return {
    destroy() {
      clearTimeout(timer);
      node.removeEventListener("keydown", onKeydown);
      if (opener instanceof HTMLElement && opener.isConnected) opener.focus({ preventScroll: true });
    },
  };
}
