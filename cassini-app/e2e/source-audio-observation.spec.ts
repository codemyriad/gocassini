import { test, expect } from "@playwright/test";
import { createServer, type ServerResponse } from "node:http";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";

// The real embedded Cassini bundle against controllable operator responses.
// This proves late uploads refresh even with a connected SSE stream and a
// finished job, and that storage is never presented as proof of ingestion.
test("recording details observe a late upload and the build that uses it", async ({ page }) => {
  const proxy = "/index.php/apps/app_api/proxy/gocassini";
  const now = "2026-09-10T10:00:00Z";
  const job = {id: "observe-job", provider: "nextcloud-talk", request_json: '{"roomToken":"room"}',
    state: "succeeded", stage: "done", current_attempt_number: 1, rerun_count: 0,
    created_at: now, updated_at: now, record_started_at: now, record_finished_at: now,
    artifact_run_path: "/run", error: null, record_exit_code: null,
    source_audio_rebuild: {upload_seq: 0, built_seq: 0, pending: false, rebuild_count: 0}};
  const audio: any = {collection_enabled: true, ingest_enabled: true, uploads: [], receiving: [],
    attempts: [{attempt: 1, available: true, participants: []}]};
  const attempts: any[] = [{...job, job_id: job.id, attempt_number: 1,
    build_started_at: now, build_finished_at: now, publish_finished_at: now}];
  const streams = new Set<ServerResponse>();
  const server = createServer(async (req, res) => {
    const route = req.url?.split("?")[0];
    if (route === `${proxy}/operator/events`) {
      res.writeHead(200, {"Content-Type": "text/event-stream"});
      res.write(": connected\n\n");
      streams.add(res);
      res.on("close", () => streams.delete(res));
      return;
    }
    if (route === `${proxy}/ui/viewer.js` || route === `${proxy}/ui/viewer.css`) {
      const ext = route.endsWith("js") ? "js" : "css";
      try {
        const asset = await readFile(fileURLToPath(new URL(`../dist/embedded/embedded.${ext}`, import.meta.url)));
        res.writeHead(200, {"Content-Type": ext === "js" ? "text/javascript" : "text/css"});
        res.end(asset);
      } catch { res.writeHead(500); res.end("Run npm run build:embedded -w cassini-app first"); }
      return;
    }
    if (route === "/") {
      res.writeHead(200, {"Content-Type": "text/html; charset=utf-8"});
      res.end(`<html><head><meta charset="utf-8"></head><body><div id="content"></div><script defer src="${proxy}/ui/viewer.js"></script></body></html>`);
      return;
    }
    let payload: unknown = {};
    if (route === `${proxy}/operator/jobs`) payload = [job];
    if (route === `${proxy}/operator/jobs/${job.id}`) payload = {job, attempts, source_audio: audio};
    if (route === `${proxy}/operator/status`) payload = {ok: true, recordings_access: {ok: true, state: "provisioned"}};
    res.writeHead(200, {"Content-Type": "application/json"});
    res.end(JSON.stringify(payload));
  });
  await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
  const address = server.address() as {port: number};
  try {
    await page.goto(`http://127.0.0.1:${address.port}/#surface=operator&job=${job.id}`);
    const panel = page.getByRole("region", {name: "Participant audio"});
    await expect(panel).toBeVisible();
    await expect(panel).toContainText("No stored uploads matched");
    await expect.poll(() => streams.size).toBeGreaterThan(0);
    audio.expected = [{owner: "alice", call_start_ms: Date.parse(now), status: "recording", updated_at: now}];
    await expect(panel).toContainText("Browser reports recording locally");
    job.state = "queued";
    job.stage = "build";
    audio.expected[0].status = "uploading";
    audio.wait_until = "2026-09-10T10:02:00Z";
    await expect(panel).toContainText("Waiting for registered captures");
    await expect(panel).toContainText("Processing will start when registered uploads arrive");
    audio.receiving = [{owner: "alice", bytes: 8192, segments: 0, complete: false}];
    await expect(panel).toContainText("Receiving alice’s audio");
    audio.receiving = [];
    audio.uploads = [{owner: "alice", bytes: 12345, segments: 2, complete: true, received_at: now}];
    job.state = "succeeded";
    job.stage = "done";
    audio.expected[0].status = "stored";
    delete audio.wait_until;
    job.source_audio_rebuild = {upload_seq: 1, built_seq: 0, pending: true, rebuild_count: 0};
    await expect(panel).toContainText("Stored");
    await expect(panel).toContainText("before rebuilding");
    await expect(panel).not.toContainText("Included in meeting audio");
    job.current_attempt_number = 2;
    job.source_audio_rebuild = {upload_seq: 1, built_seq: 1, pending: false, rebuild_count: 1};
    attempts.unshift({...attempts[0], attempt_number: 2});
    audio.attempts.unshift({attempt: 2, available: true, participants: [{owner: "alice", segments: 2,
      placed: 1, skipped: 1, spliced_ms: 12000, mix_spliced: true, transcript_source: "merged-mix",
      rejections: ["segment 1 not spliced: incomplete audio"]}]});
    await expect(panel).toContainText("Included in meeting audio");
    await expect(panel).toContainText("Used in the transcribed mix");
    await expect(panel).toContainText("12.0 seconds used");
    await expect(panel).toContainText("incomplete audio");
    await expect(panel).toContainText("Build 1 · previous");
    audio.uploads.push({...audio.uploads[0], exclusion_reason: "Overlapping captures for the same account"});
    await expect(panel).toContainText("Currently excluded: Overlapping captures for the same account");
    // One account can own concurrent audio sources; show both identities and uses.
    audio.uploads = [
      {...audio.uploads[0], call_start_ms: Date.parse(now), capture_id: "11111111-browser-a", session_ids: ["session-browser-a"]},
      {...audio.uploads[0], call_start_ms: Date.parse(now), capture_id: "22222222-browser-b", session_ids: ["session-browser-b"]},
    ];
    audio.attempts[0].participants = [
      {...audio.attempts[0].participants[0], session_id: "session-browser-a"},
      {...audio.attempts[0].participants[0], session_id: "session-browser-b", spliced_ms: 9000},
    ];
    await expect(panel).toContainText("Capture 11111111");
    await expect(panel).toContainText("Capture 22222222");
    await expect(panel.locator('[title="session-browser-a"]')).toBeVisible();
    await expect(panel.locator('[title="session-browser-b"]')).toBeVisible();
    await expect(panel).toContainText("9.0 seconds used");
    audio.expected.push({owner: "bob", call_start_ms: Date.parse(now), status: "timed_out", updated_at: now});
    await expect(panel).toContainText("Upload deadline passed; audio has not arrived");
  } finally {
    for (const stream of streams) stream.end();
    server.closeAllConnections();
    await new Promise<void>(resolve => server.close(() => resolve()));
  }
});
