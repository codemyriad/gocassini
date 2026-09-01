import { afterEach, describe, expect, it, vi } from "vitest";

import { OperatorClient } from "./client";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("operator settings client", () => {
  it("defaults transcription terms for an older settings response", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        new Response(JSON.stringify({ quality: "best" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );

    const settings = await new OperatorClient("https://operator.test").getSettings();

    expect(settings.transcription_terms).toEqual([]);
  });

  it("normalizes the effective execution view, including a CPU device", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        new Response(
          JSON.stringify({
            quality: "balanced",
            source: "auto",
            detected_gpu: false,
            cores: 4,
            effective: {
              quality: "balanced",
              device: "cpu",
              model: "parakeet-tdt-0.6b-v3-int8",
              note: "no usable GPU on this host",
            },
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      ),
    );

    const settings = await new OperatorClient("https://operator.test").getSettings();

    expect(settings.effective).toEqual({
      quality: "balanced",
      device: "cpu",
      model: "parakeet-tdt-0.6b-v3-int8",
      note: "no usable GPU on this host",
    });
  });

  it("keeps a renderable effective view when the server omits it", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        new Response(JSON.stringify({ quality: "best" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );

    const settings = await new OperatorClient("https://operator.test").getSettings();

    expect(settings.effective).toEqual({ quality: "balanced", device: "", model: "", note: "" });
  });

  it("sends and reads transcription terms through PUT settings", async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const payload = JSON.parse(String(init?.body)) as Record<string, unknown>;
      return new Response(
        JSON.stringify({
          ...payload,
          source: "user",
          transcription_terms: ["Gocassini", "Nextcloud Talk"],
        }),
        {
          status: 200,
          headers: { "Content-Type": "application/json" },
        },
      );
    });
    vi.stubGlobal("fetch", fetchMock);

    const settings = await new OperatorClient("https://operator.test").putSettings({
      quality: "balanced",
      device_override: "",
      transcription_terms: [" Gocassini ", "Nextcloud Talk", "gocassini"],
    });

    expect(fetchMock).toHaveBeenCalledOnce();
    const request = fetchMock.mock.calls[0]?.[1];
    expect(JSON.parse(String(request?.body)).transcription_terms).toEqual([
      " Gocassini ",
      "Nextcloud Talk",
      "gocassini",
    ]);
    expect(settings.transcription_terms).toEqual(["Gocassini", "Nextcloud Talk"]);
  });
});
