// An Escape that a control inside Cassini has already acted on: it cleared a
// field, closed a list or cancelled a rename. The shell, and any panel that
// closes on Escape, leaves a claimed Escape alone.
//
// Not `event.defaultPrevented`: the page Cassini is embedded in can cancel
// every Escape before Cassini sees it. In Nextcloud, the Notifications app's
// header menu does, from a capture listener on the document, so an Escape
// would never close a sheet there.

const claimed = new WeakSet<Event>();

export function claimEscape(event: KeyboardEvent): void {
  event.preventDefault();
  claimed.add(event);
}

export function escapeClaimed(event: KeyboardEvent): boolean {
  return claimed.has(event);
}
