// Props a test can change after mount(): a $state proxy, so a component reads
// the new value the way it would from a parent. Tests only.
export function reactiveProps<T extends object>(initial: T): T {
  const props = $state(initial);
  return props;
}
