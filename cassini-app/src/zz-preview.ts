// Throwaway local preview of SettingsPanel with staging-like fixtures. Not committed.
import { mount, unmount } from "svelte";
import SettingsPanel from "./SettingsPanel.svelte";
import { OperatorClient } from "./operator/client";
import "./app.css";

const rev = "r".repeat(64);
let accountExists = new URLSearchParams(location.search).get("access") !== "missing";
let settings: Record<string, unknown> = {
  quality: "best", meeting_format: "opus", transcription_enabled: true, active_model: "parakeet-tdt-0.6b-v3-int8", active_revision: rev,
  source_retention: "storage-policy", retain_video: false, cores: 8, detected_gpu: false, source: "saved",
  effective: { device: "cpu", model: "parakeet-tdt-0.6b-v3-int8", min_free_memory_mb: 0 },
};
const models = [
  { id: "parakeet-tdt-ctc-110m-en-int8", kind: "speech", name: "Parakeet TDT CTC 110M — int8", revision: rev, download_bytes: 130e6, installed_bytes: 0, installed: false, ready: false, device: "cpu" },
  { id: "parakeet-tdt-0.6b-v3-int8", kind: "speech", name: "Parakeet TDT 0.6B v3 — int8", revision: rev, download_bytes: 700e6, installed_bytes: 700e6, installed: true, ready: true, device: "cpu" },
  { id: "parakeet-tdt-0.6b-v3", kind: "speech", name: "Parakeet TDT 0.6B v3 — fp32", revision: rev, download_bytes: 2.36e9, installed_bytes: 0, installed: false, ready: false, device: "cpu" },
  { id: "nemotron-3-diarization-int8", kind: "diarization", name: "Nemotron 3 Diarization — int8", revision: rev, download_bytes: 64915668, installed_bytes: 103967426, installed: false, ready: false, device: "cpu", runtime_supported: false },
];
const jobs = [{ id: "j", model: "parakeet-tdt-0.6b-v3", revision: rev, device: "cpu", state: "cancelled", updated_at: "now", progress: { phase: "", completed_bytes: 0, total_bytes: 0, reused_bytes: 0 } }];
const realFetch = window.fetch.bind(window);
window.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
  const url = new URL(String(input instanceof Request ? input.url : input), location.href);
  if (url.origin !== location.origin || !url.pathname.startsWith("/operator")) return realFetch(input, init);
  const path = url.pathname.replace(/^\/operator/, "");
  if (path === "/settings") {
    if (init?.method === "PUT") settings = { ...settings, ...JSON.parse(String(init.body)) };
    return Response.json(settings);
  }
  if (path === "/settings/models") return Response.json({ models, jobs, downloads_allowed: true, device: "cpu" });
  if (path === "/settings/llm") return Response.json({ providers: [{ id: "or", kind: "openrouter", name: "Open Router", base_url: "https://openrouter.ai/api/v1" }], summary: { enabled: true, provider: "or" }, insights: { enabled: false } });
  if (path.startsWith("/storage/retention")) return Response.json({ recordings: { forever: true } });
  if (path === "/storage") {
    if (init?.method === "POST") accountExists = true;
    return Response.json({ ok: accountExists, detail: "", service_account: { exists: accountExists, user: "cassini" }, setup: accountExists ? [] : [{ id: "account", action: "create_user", browser: true, args: { user: "cassini" } }] });
  }
  return Response.json({});
}) as typeof fetch;

const host = document.getElementById("host")!;
let app = mount(SettingsPanel, { target: host, props: { operatorClient: new OperatorClient("/operator") } });
document.querySelectorAll<HTMLButtonElement>("button[data-w]").forEach((b) => b.onclick = () => { host.style.maxWidth = b.dataset.w + "px"; });
document.querySelectorAll<HTMLButtonElement>("button[data-t]").forEach((b) => b.onclick = () => { document.documentElement.dataset.theme = b.dataset.t; });
document.querySelectorAll<HTMLButtonElement>("button[data-a]").forEach((b) => b.onclick = () => {
  const u = new URL(location.href); u.searchParams.set("access", b.dataset.a!); location.href = u.toString();
});
void app; void unmount;
