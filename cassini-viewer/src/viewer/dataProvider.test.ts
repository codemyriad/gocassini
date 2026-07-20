import { afterEach, describe, expect, it, vi } from "vitest";

import type { MeetingCatalogEntry } from "./catalog";

// The DataProvider seam's only behaviour worth testing is the routing +
// store-threading it absorbed out of App.svelte (D-415): audioPath vs
// artifactPath dispatch, the null/throw contracts, and that each provider owns
// its own PortableMeetingStore (cache no longer implicitly module-global). We
// mock the underlying loaders so the tests assert dispatch precisely without
// standing up opus fixtures — the loaders themselves are covered by
// loadArtifact.test.ts.
// vi.mock factories are hoisted above the module body, so the mock fns must be
// created inside vi.hoisted (also hoisted) to be initialized when the factory
// runs.
const {
  loadPortableArtifactFromAudioPath,
  loadArtifactFromDirectory,
  loadPortableMeetingSummary,
  switchPortableTranscript,
  loadBundledArtifact,
  loadMeetingCatalog,
  FakePortableMeetingStore,
} = vi.hoisted(() => {
  class FakePortableMeetingStore {}
  return {
    loadPortableArtifactFromAudioPath: vi.fn((..._args: unknown[]) =>
      Promise.resolve({ kind: "portable" }),
    ),
    loadArtifactFromDirectory: vi.fn((..._args: unknown[]) =>
      Promise.resolve({ kind: "directory" }),
    ),
    loadPortableMeetingSummary: vi.fn((..._args: unknown[]) =>
      Promise.resolve({ speakerCount: 1, segmentCount: 2, digestDurationMs: 3 }),
    ),
    switchPortableTranscript: vi.fn((..._args: unknown[]) =>
      Promise.resolve({ kind: "switched" }),
    ),
    loadBundledArtifact: vi.fn((..._args: unknown[]) => Promise.resolve({ kind: "bundled" })),
    loadMeetingCatalog: vi.fn((..._args: unknown[]) =>
      Promise.resolve({ version: "cassini.viewer.catalog.v1", meetings: [] }),
    ),
    FakePortableMeetingStore,
  };
});

vi.mock("./loadArtifact", () => ({
  loadPortableArtifactFromAudioPath,
  loadArtifactFromDirectory,
  loadPortableMeetingSummary,
  switchPortableTranscript,
  loadBundledArtifact,
  PortableMeetingStore: FakePortableMeetingStore,
}));

vi.mock("./catalog", () => ({
  loadMeetingCatalog,
}));

import { StaticCatalogProvider } from "./dataProvider";

function entry(overrides: Partial<MeetingCatalogEntry>): MeetingCatalogEntry {
  return {
    id: "m1",
    title: "Meeting",
    dateLabel: "2026-03-10",
    ...overrides,
  };
}

describe("StaticCatalogProvider", () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  describe("loadMeetingForEntry", () => {
    it("routes an audioPath entry to the portable loader with the provider's store", async () => {
      const provider = new StaticCatalogProvider();
      const result = await provider.loadMeetingForEntry(entry({ audioPath: "./m1.opus" }));

      expect(result).toEqual({ kind: "portable" });
      expect(loadPortableArtifactFromAudioPath).toHaveBeenCalledTimes(1);
      expect(loadPortableArtifactFromAudioPath).toHaveBeenCalledWith(
        "./m1.opus",
        expect.any(FakePortableMeetingStore),
      );
      expect(loadArtifactFromDirectory).not.toHaveBeenCalled();
    });

    it("prefers audioPath over artifactPath when both are present", async () => {
      const provider = new StaticCatalogProvider();
      await provider.loadMeetingForEntry(entry({ audioPath: "./m1.opus", artifactPath: "./dir" }));

      expect(loadPortableArtifactFromAudioPath).toHaveBeenCalledTimes(1);
      expect(loadArtifactFromDirectory).not.toHaveBeenCalled();
    });

    it("routes an artifactPath-only entry to the directory loader", async () => {
      const provider = new StaticCatalogProvider();
      const result = await provider.loadMeetingForEntry(entry({ artifactPath: "./meetings/m1" }));

      expect(result).toEqual({ kind: "directory" });
      expect(loadArtifactFromDirectory).toHaveBeenCalledWith("./meetings/m1");
      expect(loadPortableArtifactFromAudioPath).not.toHaveBeenCalled();
    });

    it("rejects with the original message when neither path is present", async () => {
      const provider = new StaticCatalogProvider();
      await expect(
        provider.loadMeetingForEntry(entry({ id: "m9" })),
      ).rejects.toThrow("Meeting m9 is missing artifactPath and audioPath");
    });
  });

  describe("loadMeetingSummary", () => {
    it("returns null for a non-audioPath entry without calling the loader", async () => {
      const provider = new StaticCatalogProvider();
      const summary = await provider.loadMeetingSummary(entry({ artifactPath: "./dir" }));

      expect(summary).toBeNull();
      expect(loadPortableMeetingSummary).not.toHaveBeenCalled();
    });

    it("delegates to the portable summary loader with the provider's store for audioPath entries", async () => {
      const provider = new StaticCatalogProvider();
      const summary = await provider.loadMeetingSummary(entry({ audioPath: "./m1.opus" }));

      expect(summary).toEqual({ speakerCount: 1, segmentCount: 2, digestDurationMs: 3 });
      expect(loadPortableMeetingSummary).toHaveBeenCalledWith(
        "./m1.opus",
        expect.any(FakePortableMeetingStore),
      );
    });
  });

  describe("switchTranscript", () => {
    it("rejects for a non-audioPath entry", async () => {
      const provider = new StaticCatalogProvider();
      await expect(
        provider.switchTranscript(entry({ id: "m3", artifactPath: "./dir" }), "canary"),
      ).rejects.toThrow("Meeting m3 has no audioPath; cannot switch transcript");
      expect(switchPortableTranscript).not.toHaveBeenCalled();
    });

    it("delegates to switchPortableTranscript with the provider's store", async () => {
      const provider = new StaticCatalogProvider();
      const result = await provider.switchTranscript(entry({ audioPath: "./m1.opus" }), "parakeet");

      expect(result).toEqual({ kind: "switched" });
      expect(switchPortableTranscript).toHaveBeenCalledWith(
        "./m1.opus",
        "parakeet",
        expect.any(FakePortableMeetingStore),
      );
    });
  });

  it("delegates loadCatalog and loadBundledArtifact to the module loaders", async () => {
    const provider = new StaticCatalogProvider();
    await expect(provider.loadCatalog()).resolves.toEqual({
      version: "cassini.viewer.catalog.v1",
      meetings: [],
    });
    await expect(provider.loadBundledArtifact()).resolves.toEqual({ kind: "bundled" });
    expect(loadMeetingCatalog).toHaveBeenCalledTimes(1);
    expect(loadBundledArtifact).toHaveBeenCalledTimes(1);
  });

  it("gives each provider instance its own store so cache state is not shared", async () => {
    const providerA = new StaticCatalogProvider();
    const providerB = new StaticCatalogProvider();
    await providerA.loadMeetingForEntry(entry({ audioPath: "./a.opus" }));
    await providerB.loadMeetingForEntry(entry({ audioPath: "./b.opus" }));

    const storeA = loadPortableArtifactFromAudioPath.mock.calls[0]?.[1];
    const storeB = loadPortableArtifactFromAudioPath.mock.calls[1]?.[1];
    expect(storeA).toBeInstanceOf(FakePortableMeetingStore);
    expect(storeB).toBeInstanceOf(FakePortableMeetingStore);
    expect(storeA).not.toBe(storeB);
  });
});
