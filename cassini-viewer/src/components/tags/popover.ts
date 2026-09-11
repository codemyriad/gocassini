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
