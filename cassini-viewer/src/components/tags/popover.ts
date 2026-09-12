// Keyboard and pointer rules shared by the tag popovers.

// Arrow keys wrap. Left and Right only move in a grid (columns > 1), so they
// keep moving the caret in a text field.
export function stepIndex(index: number, key: string, count: number, columns = 1): number | null {
  const steps: Record<string, number> =
    columns > 1
      ? { ArrowDown: columns, ArrowUp: -columns, ArrowRight: 1, ArrowLeft: -1 }
      : { ArrowDown: 1, ArrowUp: -1 };
  const step = steps[key];
  if (step === undefined || count === 0) {
    return null;
  }
  return (((index + step) % count) + count) % count;
}

// composedPath, because inside Nextcloud's shadow root a window listener sees
// the shadow host as the target of every click.
export function isOutside(event: Event, ...inside: (Element | null | undefined)[]): boolean {
  const path = event.composedPath();
  return !inside.some((element) => element && path.includes(element));
}

type PopoverOptions = { anchor: HTMLElement | null; close: () => void };

// Pins a popover under its anchor — above it when there is no room below —
// inside the viewport, so no screen positions a popover itself. It closes on
// Esc, on a click outside, and when focus moves to something else; a click on
// the anchor is left to the anchor, which toggles it. Focus lost with the
// popover goes back to the anchor.
export function popover(node: HTMLElement, options: PopoverOptions) {
  let { anchor, close } = options;
  const place = () => {
    if (!anchor) {
      return;
    }
    const box = anchor.getBoundingClientRect();
    const gap = 4;
    const height = node.offsetHeight;
    const fitsBelow = box.bottom + gap + height <= window.innerHeight || box.top - gap - height < 0;
    node.style.position = "fixed";
    node.style.top = `${fitsBelow ? box.bottom + gap : box.top - gap - height}px`;
    node.style.left = `${Math.max(gap, Math.min(box.left, window.innerWidth - node.offsetWidth - gap))}px`;
  };
  const onPointerdown = (event: PointerEvent) => isOutside(event, node, anchor) && close();
  // Stopped here, so a popover inside another closes alone.
  const onKeydown = (event: KeyboardEvent) => {
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      anchor?.focus();
      close();
    }
  };
  // Not when focus leaves for nowhere, as it does when the window loses it.
  const onFocusout = (event: FocusEvent) => {
    const next = event.relatedTarget;
    if (next instanceof Node && !node.contains(next) && !anchor?.contains(next)) {
      close();
    }
  };
  place();
  window.addEventListener("resize", place);
  window.addEventListener("scroll", place, true);
  window.addEventListener("pointerdown", onPointerdown);
  node.addEventListener("keydown", onKeydown);
  node.addEventListener("focusout", onFocusout);
  return {
    update(next: PopoverOptions) {
      ({ anchor, close } = next);
      place();
    },
    destroy() {
      window.removeEventListener("resize", place);
      window.removeEventListener("scroll", place, true);
      window.removeEventListener("pointerdown", onPointerdown);
      const scope = anchor?.getRootNode() as Document | ShadowRoot | undefined;
      const active = scope?.activeElement;
      if (anchor?.isConnected && (!active || active === document.body || node.contains(active))) {
        anchor.focus();
      }
    },
  };
}
