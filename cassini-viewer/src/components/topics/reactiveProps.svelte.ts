// Props a test can change after mount: Svelte 5 components read them through
// this proxy, as a parent's props are read.
export function reactiveProps<T extends object>(initial: T): T {
  const props = $state(initial);
  return props;
}
