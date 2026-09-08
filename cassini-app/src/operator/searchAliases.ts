/**
 * The spellings transcription produces for a name, as an administrator types
 * them: one name per line, its spellings separated by commas.
 *
 *     Cassini, casino, casini
 *     Eisbuk, ice book
 *
 * A line is a group of equivalent terms — searching for any of them finds all
 * of them. The first is what someone would type; the rest are what transcripts
 * actually say.
 */
export function parseSearchAliases(text: string): string[][] {
  return text
    .split(/\r?\n/)
    .map((line) =>
      line
        .split(",")
        .map((part) => part.trim())
        .filter(Boolean),
    )
    .filter((group) => group.length > 0);
}

/** The inverse, for showing what is stored. */
export function formatSearchAliases(groups: string[][]): string {
  return groups.map((group) => group.join(", ")).join("\n");
}
