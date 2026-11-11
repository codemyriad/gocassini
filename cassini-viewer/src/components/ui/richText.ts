// A translated sentence with marked-up parts in it (a link, a bold phrase, a
// name as code), without {@html} and without building the sentence from
// fragments. Shared by the viewer and the shell (`cassini-viewer/ui/richText`).
//
// The dictionary holds the whole sentence with named placeholders, e.g.
// "{title} in {link}. Revoking it disconnects the agent." Call it with
// `slotArgs(["title", "link"])` (or `slotMark("link")` for one) and split the
// result: text comes back as `{ text }`, each placeholder as `{ slot, text: "" }`,
// in the order the translation puts them.
//
//   {#each richText<"link">($LL.x.step(slotArgs(["link"]))) as part}
//     {#if part.slot === "link"}<a …>…</a>{:else}{part.text}{/if}
//   {/each}
//
// With one placeholder, `around` gives the text before and after it:
//
//   $: [before, after] = around($LL.x.saved, "user");
//   {before}<code>{user}</code>{after}

const MARK = "\u0001";

export type RichPart<K extends string = string> = { text: string; slot?: K };

export function slotMark(name: string): string {
  return `${MARK}${name}${MARK}`;
}

export function slotArgs<K extends string>(names: readonly K[]): Record<K, string> {
  return Object.fromEntries(names.map((name) => [name, slotMark(name)])) as Record<K, string>;
}

export function richText<K extends string = string>(translated: string): RichPart<K>[] {
  return translated
    .split(MARK)
    .map((piece, index): RichPart<K> => (index % 2 === 1 ? { slot: piece as K, text: "" } : { text: piece }))
    .filter((part) => part.slot !== undefined || part.text !== "");
}

// The sentence cut where its one placeholder `name` goes: [before, after].
export function around<K extends string>(sentence: (args: Record<K, string>) => string, name: K): [string, string] {
  const mark = slotMark(name);
  const [before, after = ""] = sentence({ [name]: mark } as Record<K, string>).split(mark);
  return [before, after];
}
