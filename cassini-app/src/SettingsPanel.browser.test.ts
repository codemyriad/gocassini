import { mount, unmount } from "svelte";
import { afterEach, expect, it, vi } from "vitest";
import { page } from "vitest/browser";
import SettingsPanel from "./SettingsPanel.svelte";
import { OperatorClient } from "./operator/client";
import { notifyRetentionChanged } from "./operator/retentionSignal";
import type { RetentionSettings } from "./operator/retention";
import "./app.css";

let app: ReturnType<typeof mount> | undefined;
let host: HTMLDivElement;
afterEach(async () => {
  if (app) await unmount(app);
  app = undefined;
  host?.remove();
  vi.unstubAllGlobals();
});

it("saves the publication format through settings and restores it on reload", async () => {
  let saved = { quality: "balanced", meeting_format: "opus", transcription_enabled: true, active_model: "prepared", active_revision: "r1" };
  const writes: Record<string, unknown>[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), location.href).pathname;
    if (path === "/operator/settings") {
      if (init?.method === "PUT") {
        const update = JSON.parse(init.body as string);
        writes.push(update);
        saved = { ...saved, ...update };
      }
      return Response.json(saved);
    }
    if (path === "/operator/settings/models") return Response.json({ models: [], jobs: [], downloads_allowed: false });
    if (path === "/operator/settings/llm") {
      return Response.json({ providers: [], summary: { enabled: false }, insights: { enabled: false } });
    }
    return Response.json({});
  }));
  host = document.createElement("div");
  document.body.append(host);
  const open = () => app = mount(SettingsPanel, { target: host, props: { operatorClient: new OperatorClient("/operator") } });
  open();
  const format = page.getByRole("combobox", { name: "Published meeting" });
  await expect.element(format).toHaveValue("opus");
  await format.selectOptions("json");
  expect(writes).toHaveLength(0);
  // The card reads: transcript, summary, what is published, then what stays on the server.
  const card = host.querySelector('[aria-labelledby="recording-publication-heading"]')!;
  const order = ['[aria-label="Transcription settings"]', '[aria-labelledby="summary-heading"]', "#meeting-format", "fieldset"].map((selector) => card.querySelector(selector));
  expect(order.every(Boolean)).toBe(true);
  for (let i = 1; i < order.length; i++) {
    expect(order[i - 1]!.compareDocumentPosition(order[i]!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  }
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect.poll(() => saved.meeting_format).toBe("json");
  expect(writes[0].meeting_format).toBe("json");
  await unmount(app!);
  app = undefined;
  open();
  await expect.element(format).toHaveValue("json");
  await format.selectOptions("opus");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect.poll(() => saved.meeting_format).toBe("opus");
});

it("keeps publication separate from source retention and explicitly opts into deletion", async () => {
  let saved = { quality: "balanced", meeting_format: "opus", source_retention: "storage-policy", retain_video: true, transcription_enabled: true, active_model: "prepared", active_revision: "r1" };
  const writes: Record<string, unknown>[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), location.href).pathname;
    if (path === "/operator/settings") {
      if (init?.method === "PUT") {
        const update = JSON.parse(init.body as string); writes.push(update); saved = { ...saved, ...update };
      }
      return Response.json(saved);
    }
    if (path === "/operator/settings/models") return Response.json({ models: [], jobs: [] });
    if (path === "/operator/settings/llm") return Response.json({ providers: [], summary: { enabled: false }, insights: { enabled: false } });
    return Response.json({});
  }));
  host = document.createElement("div"); document.body.append(host);
  const open = () => app = mount(SettingsPanel, { target: host, props: { operatorClient: new OperatorClient("/operator") } });
  open();
  const nothing = page.getByRole("radio", { name: "Nothing", exact: true });
  await expect.element(page.getByRole("radio", { name: "Audio and video", exact: true })).toBeChecked();
  expect(writes).toHaveLength(0);
  // Publishing audio needs audio kept, so Nothing waits for a transcript-only format.
  await expect.element(nothing).toBeDisabled();
  await expect.element(page.getByText("Unavailable while audio is published.")).toBeVisible();
  await page.getByRole("combobox", { name: "Published meeting" }).selectOptions("json");
  await expect.element(nothing).toBeEnabled();
  await nothing.click();
  expect(writes).toHaveLength(0);
  await expect.element(page.getByRole("radio", { name: "Audio and video", exact: true })).not.toBeChecked();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect.poll(() => saved.source_retention).toBe("delete-after-processing");
  expect(writes[0]).toMatchObject({ meeting_format: "json", source_retention: "delete-after-processing", retain_video: false, transcription_enabled: true });
  await unmount(app!); app = undefined; open();
  await expect.element(nothing).toBeChecked();
  // Choosing audio while Nothing is kept falls back to keeping audio only.
  await page.getByRole("combobox", { name: "Published meeting" }).selectOptions("opus");
  await expect.element(page.getByRole("radio", { name: "Audio only", exact: true })).toBeChecked();
  await page.getByRole("combobox", { name: "Published meeting" }).selectOptions("json");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect.poll(() => saved.source_retention).toBe("storage-policy");
  expect(saved.meeting_format).toBe("json");
});

it("requires prepared transcription before saving deletion", async () => {
  let saved: Record<string, unknown> = { quality: "balanced", meeting_format: "json", source_retention: "delete-after-processing", transcription_enabled: false, transcription_terms: [] };
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = new URL(String(input), location.href).pathname;
    if (path === "/operator/settings") return Response.json(saved);
    if (path === "/operator/settings/models") return Response.json({ models: [], jobs: [] });
    return Response.json({});
  }));
  host = document.createElement("div"); document.body.append(host);
  const open = () => app = mount(SettingsPanel, { target: host, props: { operatorClient: new OperatorClient("/operator") } });
  // An older save: transcript only and nothing kept, with transcription off.
  open();
  await expect.element(page.getByText("Saved settings publish transcript-only meetings with no transcript. Save to publish the audio instead.")).toBeVisible();
  await expect.element(page.getByText("Transcription must be on to keep nothing.")).toBeVisible();
  await page.getByPlaceholder(/^Gocassini/).fill("Cassini");
  await expect.element(page.getByRole("button", { name: "Save", exact: true })).toBeDisabled();

  // Saved on and keeping nothing; switching transcription off in the draft.
  await unmount(app!); app = undefined;
  saved = { ...saved, transcription_enabled: true, active_model: "m", active_revision: "r1" };
  open();
  await page.getByRole("checkbox", { name: "Transcribe every recording" }).click();
  await expect.element(page.getByText("Transcription is off. Turn it on above to include a transcript.")).toBeVisible();
  await expect.element(page.getByText("Transcription must be on to keep nothing.")).toBeVisible();
  await expect.element(page.getByRole("button", { name: "Save", exact: true })).toBeDisabled();
  await page.getByRole("button", { name: "Set up transcription" }).click();
  await expect.element(page.getByRole("region", { name: "Transcription settings" })).toHaveFocus();
});

it("turns transcription on from the heading switch and shows the model as one row", async () => {
  let saved: Record<string, unknown> = { quality: "balanced", meeting_format: "opus", transcription_enabled: false };
  const writes: Record<string, unknown>[] = [];
  const balanced = { id: "parakeet-ultra-0.6b-int8", name: "Parakeet balanced", description: "", revision: "r1",
    download_bytes: 700 * 1024 * 1024, installed_bytes: 0, installed: true, ready: true, device: "cpu" };
  const best = { ...balanced, id: "parakeet-ultra-0.6b", name: "Parakeet best", download_bytes: 2.2 * 1024 ** 3, installed: false, ready: false };
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), location.href).pathname;
    if (path === "/operator/settings") {
      if (init?.method === "PUT") { const update = JSON.parse(init.body as string); writes.push(update); saved = { ...saved, ...update }; }
      return Response.json(saved);
    }
    if (path === "/operator/settings/models") return Response.json({ models: [balanced, best], downloads_allowed: true, device: "cpu",
      jobs: [{ id: "j1", model: best.id, revision: "r1", device: "cpu", state: "cancelled", updated_at: "", progress: { phase: "", completed_bytes: 0, total_bytes: 0, reused_bytes: 0 } }] });
    if (path === "/operator/settings/llm") return Response.json({ providers: [], summary: { enabled: false }, insights: { enabled: false } });
    return Response.json({});
  }));
  host = document.createElement("div"); document.body.append(host);
  app = mount(SettingsPanel, { target: host, props: { operatorClient: new OperatorClient("/operator") } });
  const toggle = page.getByRole("checkbox", { name: "Transcribe every recording" });
  await expect.element(page.getByText("Parakeet balanced")).toBeVisible();
  await expect.element(toggle).toBeEnabled();
  await toggle.click();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect.poll(() => writes.at(-1)).toMatchObject({ transcription_enabled: true, active_model: balanced.id, active_revision: "r1" });

  await page.getByRole("radio", { name: /^Best/ }).click();
  await expect.element(page.getByText("Parakeet best")).toBeVisible();
  await expect.element(page.getByText("2.2 GiB")).toBeVisible();
  await expect.element(page.getByText("Download cancelled. Retry resumes it.")).toBeVisible();
  await expect.element(page.getByRole("button", { name: "Retry", exact: true })).toBeVisible();
});

it("shows the saved settings in the status strip and moves only after Save", async () => {
  let saved: Record<string, unknown> = { quality: "balanced", meeting_format: "opus", transcription_enabled: true, active_model: "parakeet-ultra-0.6b-int8", active_revision: "r1",
    source_retention: "storage-policy", retain_video: false, effective: { device: "cpu", model: "", min_free_memory_mb: 0, note: "" } };
  const balanced = { id: "parakeet-ultra-0.6b-int8", name: "Parakeet balanced", description: "", revision: "r1",
    download_bytes: 1, installed_bytes: 1, installed: true, ready: true, device: "cpu" };
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), location.href).pathname;
    if (path === "/operator/settings") {
      if (init?.method === "PUT") saved = { ...saved, ...JSON.parse(init.body as string) };
      return Response.json(saved);
    }
    if (path === "/operator/settings/models") return Response.json({ models: [balanced], jobs: [], downloads_allowed: true, device: "cpu" });
    if (path === "/operator/settings/llm") return Response.json({ providers: [{ id: "or", name: "OpenRouter", base_url: "", model: "" }], summary: { enabled: true, provider: "or", model: "", template: "" }, insights: { enabled: false } });
    if (path === "/operator/storage/retention") return Response.json({ recordings: { forever: false, count: 30, unit: "days" } });
    return Response.json({});
  }));
  host = document.createElement("div"); document.body.append(host);
  app = mount(SettingsPanel, { target: host, props: { operatorClient: new OperatorClient("/operator") } });
  const strip = page.getByRole("list", { name: "Current settings" });
  await expect.element(strip.getByRole("button", { name: /^Transcribe/ })).toHaveTextContent("On · Balanced · CPU");
  await expect.element(strip.getByRole("button", { name: /^Summarise/ })).toHaveTextContent("On · OpenRouter");
  await expect.element(strip.getByRole("button", { name: /^Keep on server/ })).toHaveTextContent("Audio only · 30 days");
  const publish = strip.getByRole("button", { name: /^Publish/ });
  await expect.element(publish).toHaveTextContent("Audio + transcript");

  await strip.getByRole("button", { name: /^Transcribe/ }).click();
  await expect.element(page.getByRole("region", { name: "Transcription settings" })).toHaveFocus();
  await strip.getByRole("button", { name: /^Summarise/ }).click();
  const question = page.getByRole("region", { name: "Summary", exact: true });
  await expect.element(question).toHaveFocus();
  await expect.element(question.getByRole("combobox", { name: "Provider" })).toHaveValue("or");

  const transcriptionSwitch = page.getByRole("checkbox", { name: "Transcribe every recording" });
  const summarySwitch = page.getByRole("checkbox", { name: "Summarise every meeting" });
  const provider = question.getByRole("combobox", { name: "Provider" });
  await transcriptionSwitch.click();
  for (const control of [summarySwitch, provider]) await expect.element(control).toBeDisabled();
  await expect.element(strip.getByRole("button", { name: /^Transcribe/ })).toHaveTextContent("On · Balanced · CPU");
  await transcriptionSwitch.click();
  for (const control of [summarySwitch, provider]) await expect.element(control).toBeEnabled();
  await expect.element(provider).toHaveValue("or");

  await page.getByRole("combobox", { name: "Published meeting" }).selectOptions("json");
  await expect.element(publish).toHaveTextContent("Audio + transcript");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect.element(publish).toHaveTextContent("Transcript only");
});

it("says how long kept media stays from the Recordings policy and follows the retention editor", async () => {
  let retention = true;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = new URL(String(input), location.href).pathname;
    if (path === "/operator/settings") return Response.json({ quality: "balanced", meeting_format: "opus", source_retention: "storage-policy", retain_video: false, transcription_enabled: true, active_model: "m", active_revision: "r1" });
    if (path === "/operator/settings/models") return Response.json({ models: [], jobs: [] });
    if (path === "/operator/storage/retention") return retention ? Response.json({ recordings: { forever: false, count: 30, unit: "days" } }) : new Response("down", { status: 503 });
    return Response.json({});
  }));
  host = document.createElement("div"); document.body.append(host);
  const open = () => app = mount(SettingsPanel, { target: host, props: { operatorClient: new OperatorClient("/operator") } });
  open();
  await expect.element(page.getByText("Audio only, for 30 days.")).toBeVisible();
  await expect.element(page.getByText("Audio and video, for 30 days. Uses the most space.")).toBeVisible();
  await expect.element(page.getByText("The 30 days come from Storage.")).toBeVisible();

  notifyRetentionChanged({ recordings: { forever: true } } as RetentionSettings);
  await expect.element(page.getByText("Audio only, kept forever.")).toBeVisible();
  await expect.element(page.getByText("Kept forever: change this in Storage.")).toBeVisible();
  await expect.element(page.getByRole("button", { name: /^Keep on server/ })).toHaveTextContent("Audio only · forever");

  await unmount(app!); app = undefined; retention = false; open();
  await expect.element(page.getByText("How long kept media stays is set in Storage.")).toBeVisible();
  await expect.element(page.getByRole("button", { name: /^Keep on server/ })).toHaveTextContent(/Audio only$/);
});

it("enables and disables dependent settings with the draft transcription switch", async () => {
  let saved: Record<string, unknown> = { quality: "balanced", meeting_format: "opus", transcription_enabled: false };
  const writes: Record<string, unknown>[] = [];
  const balanced = { id: "parakeet-ultra-0.6b-int8", name: "Parakeet balanced", description: "", revision: "r1",
    download_bytes: 1, installed_bytes: 1, installed: true, ready: true, device: "cpu" };
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), location.href).pathname;
    if (path === "/operator/settings") {
      if (init?.method === "PUT") { const update = JSON.parse(init.body as string); writes.push(update); saved = { ...saved, ...update }; }
      return Response.json(saved);
    }
    if (path === "/operator/settings/models") return Response.json({ models: [balanced], jobs: [], downloads_allowed: true, device: "cpu" });
    if (path === "/operator/settings/llm") return Response.json({ providers: [], summary: { enabled: false }, insights: { enabled: false } });
    return Response.json({});
  }));
  host = document.createElement("div"); document.body.append(host);
  app = mount(SettingsPanel, { target: host, props: { operatorClient: new OperatorClient("/operator") } });
  const offLine = page.getByText("Transcription needs to be on to edit the settings below.");
  const summarySwitch = page.getByRole("checkbox", { name: "Summarise every meeting" });
  const format = page.getByRole("combobox", { name: "Published meeting" });
  const audioOnly = page.getByRole("radio", { name: "Audio only", exact: true });
  const addOne = page.getByRole("button", { name: "Add one", exact: true });
  await expect.element(offLine).toBeVisible();
  expect(host.querySelector(".q-callout svg")).not.toBeNull();
  await expect.element(page.getByRole("list", { name: "Current settings" })).toBeVisible();
  await expect.element(page.getByRole("button", { name: /^Transcribe/ })).toHaveTextContent("Off");
  for (const control of [summarySwitch, addOne, format, audioOnly]) await expect.element(control).toBeDisabled();
  // The chips still jump to the dimmed questions.
  await page.getByRole("button", { name: /^Publish/ }).click();
  await expect.element(page.getByRole("region", { name: "What participants receive" })).toHaveFocus();

  await page.getByRole("checkbox", { name: "Transcribe every recording" }).click();
  for (const control of [addOne, format, audioOnly]) await expect.element(control).toBeEnabled();
  await expect.element(summarySwitch).toBeDisabled(); // A provider is still required.
  expect(writes).toHaveLength(0);
  await expect.element(page.getByRole("button", { name: /^Transcribe/ })).toHaveTextContent("Off");
  await page.getByRole("checkbox", { name: "Transcribe every recording" }).click();
  for (const control of [summarySwitch, addOne, format, audioOnly]) await expect.element(control).toBeDisabled();
  await expect.element(offLine).toBeVisible();
  expect(writes).toHaveLength(0);
  await page.getByRole("checkbox", { name: "Transcribe every recording" }).click();
  for (const control of [addOne, format, audioOnly]) await expect.element(control).toBeEnabled();
  await expect.element(offLine).not.toBeInTheDocument();
  await expect.element(page.getByText("Save to set up the summary and what participants receive.")).not.toBeInTheDocument();

  // Turning transcription off publishes audio again, whatever the select said,
  // and audio published means something must be kept.
  await page.getByRole("combobox", { name: "Published meeting" }).selectOptions("json");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect.poll(() => saved.meeting_format).toBe("json");
  await page.getByRole("checkbox", { name: "Transcribe every recording" }).click();
  await expect.element(page.getByRole("radio", { name: "Nothing", exact: true })).toBeDisabled();
  await expect.element(page.getByText("Unavailable while audio is published.")).toBeVisible();
  for (const control of [summarySwitch, addOne, format, audioOnly]) await expect.element(control).toBeDisabled();
  await expect.element(offLine).toBeVisible();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect.poll(() => writes.at(-1)).toMatchObject({ transcription_enabled: false, meeting_format: "opus" });
  await expect.element(offLine).toBeVisible();
});

it("opens the hardware facts under Change and sends Add one to the providers", async () => {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = new URL(String(input), location.href).pathname;
    if (path === "/operator/settings") return Response.json({ quality: "balanced", meeting_format: "opus", transcription_enabled: true, active_model: "m", active_revision: "r1",
      detected_gpu: false, cores: 8, source: "auto", device_override: "", effective: { device: "cpu", model: "", min_free_memory_mb: 0, note: "" } });
    if (path === "/operator/settings/models") return Response.json({ models: [], jobs: [] });
    if (path === "/operator/settings/llm") return Response.json({ providers: [], summary: { enabled: false }, insights: { enabled: false } });
    return Response.json({});
  }));
  const openProviders = vi.fn();
  host = document.createElement("div"); document.body.append(host);
  app = mount(SettingsPanel, { target: host, props: { operatorClient: new OperatorClient("/operator") }, events: { openProviders } });
  await expect.element(page.getByText("Transcription runs on the CPU (no GPU found).", { exact: true })).toBeVisible();
  const device = page.getByRole("combobox", { name: "Device" });
  expect(host.querySelector("#stt-device")).toBeNull();
  const change = page.getByRole("button", { name: "Hardware", exact: true });
  await change.click();
  await expect.element(change).toHaveAttribute("aria-expanded", "true");
  await expect.element(device).toBeVisible();
  await expect.element(page.getByText("8", { exact: true })).toBeVisible();

  await expect.element(page.getByText("Needs a provider.")).toBeVisible();
  await page.getByRole("button", { name: "Add one", exact: true }).click();
  expect(openProviders).toHaveBeenCalledTimes(1);

  const hints = page.getByRole("region", { name: "Transcript hints" });
  await expect.element(hints.getByRole("textbox", { name: /One term per line/ })).toBeVisible();
  await expect.element(hints.getByRole("textbox", { name: /One name per line/ })).toBeVisible();
});
