// What a public embed may do to the page around it (D-838).
//
// Inside Nextcloud, MeetingView owns the whole viewport, so a window-level
// Space shortcut and scrollIntoView are exactly right. On someone else's page
// they are not ours: Space scrolls that page, and scrollIntoView scrolls every
// ancestor, which drags the reader away from the article the recording sits in.
// These are the two decisions MeetingView asks when surface is "embed". They
// are kept pure so they can be tested without a DOM.

/**
 * Whether a key event came from inside the viewer. composedPath() crosses the
 * embed's open shadow root, so a keypress on a transcript word counts and one
 * on the host page's own content does not.
 */
export function eventIsInside(event: Pick<Event, "composedPath">, root: EventTarget | undefined): boolean {
  return root !== undefined && event.composedPath().includes(root);
}

export interface VerticalBox {
  top: number;
  bottom: number;
}

/**
 * Where to scroll the viewer's own pane so that the playing turn is in view,
 * or null when it already is. The turn lands a third of the way down, which
 * is where scrollIntoView({ block: "center" }) put it minus the header the
 * pane scrolls under. Nothing outside the pane moves.
 */
export function followScrollTop(
  pane: VerticalBox,
  element: VerticalBox,
  paneScrollTop: number,
  margin = 24,
): number | null {
  if (element.top >= pane.top + margin && element.bottom <= pane.bottom - margin) {
    return null;
  }
  const height = pane.bottom - pane.top;
  return Math.max(0, paneScrollTop + element.top - pane.top - height / 3);
}

/**
 * Whether the viewer may read and write the page's URL fragment. Inside the
 * app it is the viewer's own route (hashRouting.ts). On a host page it holds
 * that page's anchors: an embed that rewrote it would replace the reader's
 * #section with #tx=…, and one that read it would take a stray tx or t there
 * as its own.
 */
export function ownsLocationHash(surface: "app" | "embed"): boolean {
  return surface === "app";
}
