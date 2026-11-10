// A translated sentence with marked-up parts in it (a link, a bold phrase),
// without {@html} and without building the sentence from fragments.
//
// The dictionary holds the whole sentence with named placeholders, e.g.
// "{title} in {link}. Revoking it disconnects the agent." Call it with
// `slotArgs(["title", "link"])` and split the result: text pieces come back as
// `{ text }`, placeholders as `{ slot }`, in the order the translation puts them.
//
//   {#each richText($LL.x.step({ ...slotArgs(["link"]) })) as part}
//     {#if part.slot === "link"}<a …>…</a>{:else}{part.text}{/if}
//   {/each}

const MARK = "\u0001";

export type RichPart = { text: string; slot?: undefined } | { slot: string; text?: undefined };

export function slotArgs<K extends string>(names: readonly K[]): Record<K, string> {
  return Object.fromEntries(names.map((name) => [name, `${MARK}${name}${MARK}`])) as Record<K, string>;
}

export function richText(translated: string): RichPart[] {
  return translated
    .split(MARK)
    .map((piece, index): RichPart => (index % 2 === 1 ? { slot: piece } : { text: piece }))
    .filter((part) => part.slot !== undefined || part.text !== "");
}
