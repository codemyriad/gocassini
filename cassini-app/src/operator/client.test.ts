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
      model_override: "",
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

describe("llm settings client", () => {
  it("normalizes a drifted response and never carries a raw key", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        new Response(
          JSON.stringify({
            providers: [
              {
                id: "default",
                name: "OpenRouter",
                base_url: "https://openrouter.ai/api/v1",
                api_key_configured: true,
                api_key: "LEAKED-NEVER-CARRY",
              },
            ],
            readable: null,
            summary: { enabled: true, provider: "default" },
            timeout_sec: -5,
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      ),
    );

    const settings = await new OperatorClient("https://operator.test").getLLMSettings();

    expect(JSON.stringify(settings)).not.toContain("LEAKED-NEVER-CARRY");
    expect(settings.providers[0]?.api_key_configured).toBe(true);
    expect(settings.readable).toEqual({ enabled: false, provider: "", model: "" });
    expect(settings.summary.enabled).toBe(true);
    expect(settings.timeout_sec).toBe(0);
    expect(settings.effective).toEqual({ readable: null, summary: null });
  });

  it("omits api_key when unchanged and sends an empty string to clear it", async () => {
    const fetchMock = vi.fn(async () =>
      new Response(JSON.stringify({ providers: [] }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await new OperatorClient("https://operator.test").putLLMSettings({
      providers: [
        { id: "keep", name: "Keep", base_url: "https://a.test/v1" },
        { id: "clear", name: "Clear", base_url: "https://b.test/v1", api_key: "" },
        { id: "set", name: "Set", base_url: "https://c.test/v1", api_key: "new-key" },
      ],
    });

    const body = JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body)) as {
      providers: Record<string, unknown>[];
    };
    expect("api_key" in (body.providers[0] ?? {})).toBe(false);
    expect(body.providers[1]?.api_key).toBe("");
    expect(body.providers[2]?.api_key).toBe("new-key");
  });

  it("lists provider models tolerantly", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        new Response(
          JSON.stringify({
            provider: "default",
            models: [{ id: "alpha", context_length: 8192 }, { bogus: true }, { id: "" }, null],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      ),
    );

    const models = await new OperatorClient("https://operator.test").listProviderModels("default");

    expect(models).toEqual([{ id: "alpha", name: undefined, context_length: 8192 }]);
  });
});
