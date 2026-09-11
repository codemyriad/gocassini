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

// Pins a popover under its anchor — above it when there is no room below — and
// keeps it inside the viewport, so no screen positions a popover itself.
export function anchored(node: HTMLElement, anchor: HTMLElement | null) {
  let current = anchor;
  const place = () => {
    if (!current) {
      return;
    }
    const box = current.getBoundingClientRect();
    const gap = 4;
    const height = node.offsetHeight;
    const fitsBelow = box.bottom + gap + height <= window.innerHeight || box.top - gap - height < 0;
    node.style.position = "fixed";
    node.style.top = `${fitsBelow ? box.bottom + gap : box.top - gap - height}px`;
    node.style.left = `${Math.max(gap, Math.min(box.left, window.innerWidth - node.offsetWidth - gap))}px`;
  };
  place();
  window.addEventListener("resize", place);
  window.addEventListener("scroll", place, true);
  return {
    update(next: HTMLElement | null) {
      current = next;
      place();
    },
    destroy() {
      window.removeEventListener("resize", place);
      window.removeEventListener("scroll", place, true);
    },
  };
}

// composedPath, because inside Nextcloud's shadow root a window listener sees
// the shadow host as the target of every click.
export function isOutside(event: Event, ...inside: (Element | null | undefined)[]): boolean {
  const path = event.composedPath();
  return !inside.some((element) => element && path.includes(element));
}
