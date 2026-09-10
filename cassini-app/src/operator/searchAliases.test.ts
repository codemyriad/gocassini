import { describe, expect, it } from "vitest";

import { formatSearchAliases, parseSearchAliases } from "./searchAliases";

describe("parseSearchAliases", () => {
  it("reads one name per line with its spellings", () => {
    expect(parseSearchAliases("Cassini, casino, casini\nEisbuk, ice book")).toEqual([
      ["Cassini", "casino", "casini"],
      ["Eisbuk", "ice book"],
    ]);
  });

  it("keeps multi-word spellings intact", () => {
    // "next cloud talk" is one spelling, not three — splitting on whitespace
    // would make the whole feature useless for the names most likely to be
    // misheard.
    expect(parseSearchAliases("Nextcloud Talk, next cloud talk")).toEqual([
      ["Nextcloud Talk", "next cloud talk"],
    ]);
  });

  it("drops blank lines and stray separators rather than sending empty groups", () => {
    expect(parseSearchAliases("\nCassini, , casino\n   \n,,\n")).toEqual([["Cassini", "casino"]]);
  });

  // A name with nothing after it expands to nothing. It is the natural state of
  // a row someone has started, so it must not be an error while typing — the
  // server drops it too.
  it("keeps a half-typed row out of the payload without complaining", () => {
    expect(parseSearchAliases("Cassini")).toEqual([["Cassini"]]);
    expect(parseSearchAliases("")).toEqual([]);
  });

  it("round-trips what the server returns", () => {
    const groups = [
      ["Cassini", "casino"],
      ["Eisbuk", "ice book"],
    ];
    expect(parseSearchAliases(formatSearchAliases(groups))).toEqual(groups);
  });
});
