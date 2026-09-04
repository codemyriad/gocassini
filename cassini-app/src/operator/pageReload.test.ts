import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { readStorageFlash, writeStorageFlash } from "./pageReload";

// The flash exists because the fix for the stale-setup-warning papercut is a
// full page reload, and a reload would otherwise swallow the one sentence the
// administrator just earned ("5 recordings were copied into …").

function fakeSession(): Storage {
  const data = new Map<string, string>();
  return {
    get length() {
      return data.size;
    },
    clear: () => data.clear(),
    getItem: (key: string) => data.get(key) ?? null,
    key: (index: number) => [...data.keys()][index] ?? null,
    removeItem: (key: string) => void data.delete(key),
    setItem: (key: string, value: string) => void data.set(key, value),
  } as Storage;
}

describe("storage flash", () => {
  beforeEach(() => {
    vi.stubGlobal("sessionStorage", fakeSession());
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("carries a message across a reload", () => {
    writeStorageFlash({ tone: "success", message: "Storage is now access controlled." });
    expect(readStorageFlash()).toMatchObject({
      tone: "success",
      message: "Storage is now access controlled.",
    });
  });

  // Read-and-clear. Without it, every later reload of the Setup tab would
  // re-announce a switch that happened once, days ago.
  it("is shown once", () => {
    writeStorageFlash({ tone: "success", message: "Setup finished." });
    expect(readStorageFlash()).not.toBeNull();
    expect(readStorageFlash()).toBeNull();
  });

  it("has nothing to show when nothing was stashed", () => {
    expect(readStorageFlash()).toBeNull();
  });

  // sessionStorage is shared with Nextcloud's own page and every other app on
  // it, so anything already under the key could be someone else's.
  it("ignores a value it did not write", () => {
    sessionStorage.setItem("cassini.storage.flash", "not json");
    expect(readStorageFlash()).toBeNull();

    sessionStorage.setItem("cassini.storage.flash", JSON.stringify({ tone: "success" }));
    expect(readStorageFlash()).toBeNull();
  });

  it("defaults an unknown tone to success rather than dropping the message", () => {
    sessionStorage.setItem(
      "cassini.storage.flash",
      JSON.stringify({ tone: "explosive", message: "Done." }),
    );
    expect(readStorageFlash()).toMatchObject({ tone: "success", message: "Done." });
  });

  // A browser with storage blocked throws on access, not only on use. Losing the
  // sentence is cosmetic; throwing here would break the action that wrote it.
  it("survives a sessionStorage that throws", () => {
    vi.stubGlobal("sessionStorage", {
      getItem() {
        throw new Error("blocked");
      },
      setItem() {
        throw new Error("blocked");
      },
      removeItem() {
        throw new Error("blocked");
      },
    } as unknown as Storage);

    expect(() => writeStorageFlash({ tone: "success", message: "Done." })).not.toThrow();
    expect(readStorageFlash()).toBeNull();
  });
});
