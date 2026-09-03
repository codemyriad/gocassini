#!/usr/bin/env node
// Drive the browser-only half of the real Talk source-capture harness leg.
// The shell orchestrator owns Nextcloud, Talk, the ExApp, and disk assertions;
// this process owns only browser interaction and browser-side evidence.
//
// Capture follows Talk's official recording, so BOTH participants are subjects:
// Alice and Bob each record their own microphone and each upload their own
// capture under their own Nextcloud identity. Neither browser is asked anything
// and neither stores anything to say it was captured. Alice additionally
// switches microphone mid-call, which is the one thing that cuts a capture into
// several segments.

import fs from "node:fs/promises";
import path from "node:path";
import { chromium } from "@playwright/test";

const requiredEnv = [
  "NEXTCLOUD_URL",
  "ROOM_TOKEN",
  "ALICE_PASSWORD",
  "BOB_PASSWORD",
  "BROWSER_LOG_DIR",
];
for (const name of requiredEnv) {
  if (!process.env[name]) throw new Error(`${name} is required`);
}

const baseURL = process.env.NEXTCLOUD_URL.replace(/\/+$/, "");
const roomToken = process.env.ROOM_TOKEN;
const logDir = process.env.BROWSER_LOG_DIR;
const timeout = Number(process.env.BROWSER_TIMEOUT_MS || 120_000);
const resultPath = path.join(logDir, "result.json");
const pendingEvidence = new Set();
const evidenceErrors = [];

await fs.mkdir(logDir, { recursive: true });

const result = {
  result: "failed",
  startedAt: new Date().toISOString(),
  roomToken,
  alice: {},
  bob: {},
};

function assert(value, message) {
  if (!value) throw new Error(message);
}

const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const writeJSON = (name, value) => fs.writeFile(path.join(logDir, name), `${JSON.stringify(value, null, 2)}\n`);

function retain(promise) {
  pendingEvidence.add(promise);
  void promise.then(
    () => pendingEvidence.delete(promise),
    (error) => {
      evidenceErrors.push(error);
      pendingEvidence.delete(promise);
    },
  );
}

async function drainEvidence() {
  while (pendingEvidence.size > 0) {
    await Promise.allSettled([...pendingEvidence]);
  }
}

function attachEvidence(page, participant) {
  let responseIndex = 0;
  const uploadResponses = [];
  const uploadRequests = [];
  const append = (name, value) => {
    retain(fs.appendFile(path.join(logDir, `${participant}-${name}.log`), `${value}\n`));
  };

  page.on("console", (message) => append("console", `[${message.type()}] ${message.text()}`));
  page.on("pageerror", (error) => append("page-errors", error.stack || error.message));
  page.on("request", (request) => {
    if (request.method() === "POST" && request.url().includes("/operator/capture/upload")) {
      uploadRequests.push({ method: request.method(), url: request.url() });
    }
  });
  page.on("requestfailed", (request) => append(
    "request-failures",
    `${request.method()} ${request.url()} ${request.failure()?.errorText || "failed"}`,
  ));
  page.on("response", (response) => {
    const request = response.request();
    const isCaptureUpload = request.method() === "POST"
      && response.url().includes("/operator/capture/upload");
    append("network", JSON.stringify({
      method: request.method(),
      resourceType: request.resourceType(),
      status: response.status(),
      url: response.url(),
    }));

    // Preserve every body on the explicit OCS/proxy seam under test. Saving
    // every JS/image body would add megabytes of unrelated Talk assets.
    if (!response.url().includes("/ocs/") && !response.url().includes("/apps/app_api/proxy/gocassini/")) return;
    const index = responseIndex++;
    const uploadIndex = uploadResponses.length;
    const bodyName = isCaptureUpload
      ? `${participant}-upload-response-${String(uploadIndex).padStart(3, "0")}.body`
      : `${participant}-response-${String(index).padStart(4, "0")}.body`;
    const metadataName = `${participant}-response-${String(index).padStart(4, "0")}.json`;
    const capture = (async () => {
      // Chromium DevTools can leave response.body() pending forever after the
      // page's own fetch consumer reads a streamed AppAPI upload response. The
      // init-script fetch observer clones that one body inside the page; keep
      // this event's metadata and let persistObservedUploadBodies write it.
      if (isCaptureUpload) {
        uploadResponses.push({ status: response.status(), url: response.url(), bodyFile: bodyName });
        await writeJSON(metadataName, {
          method: request.method(),
          status: response.status(),
          url: response.url(),
          bodyFile: bodyName,
        });
        return;
      }
      let body;
      try {
        body = await response.body();
      } catch (error) {
        body = Buffer.from(`response body unavailable: ${error}`);
      }
      await Promise.all([
        fs.writeFile(path.join(logDir, bodyName), body),
        writeJSON(metadataName, {
          method: request.method(),
          status: response.status(),
          url: response.url(),
          bodyFile: bodyName,
        }),
      ]);
    })();
    retain(capture);
  });
  return { uploadRequests, uploadResponses };
}

// Test-only observation. It does not add hooks to the companion payload or
// Talk: the browser context records calls that the real implementations make.
async function installObservation(context) {
  await context.addInitScript(() => {
    const observation = { peerConnections: [], replacements: [], uploadResponses: [] };
    Object.defineProperty(globalThis, "__cassiniBrowserCallE2E", { value: observation });

    const nativeFetch = globalThis.fetch.bind(globalThis);
    globalThis.fetch = async (...args) => {
      const response = await nativeFetch(...args);
      const request = args[0];
      const init = args[1];
      const method = String(init?.method || request?.method || "GET").toUpperCase();
      if (method === "POST" && response.url.includes("/operator/capture/upload")) {
        const entry = { status: response.status, url: response.url, body: null, error: null };
        observation.uploadResponses.push(entry);
        try {
          void response.clone().text().then(
            (body) => { entry.body = body; },
            (error) => { entry.error = String(error); },
          );
        } catch (error) {
          entry.error = String(error);
        }
      }
      return response;
    };

    const pcPrototype = RTCPeerConnection.prototype;
    const nativeAddTrack = pcPrototype.addTrack;
    pcPrototype.addTrack = function (...args) {
      if (!observation.peerConnections.includes(this)) observation.peerConnections.push(this);
      return nativeAddTrack.apply(this, args);
    };
    const nativeAddTransceiver = pcPrototype.addTransceiver;
    pcPrototype.addTransceiver = function (...args) {
      if (!observation.peerConnections.includes(this)) observation.peerConnections.push(this);
      return nativeAddTransceiver.apply(this, args);
    };

    const nativeReplaceTrack = RTCRtpSender.prototype.replaceTrack;
    RTCRtpSender.prototype.replaceTrack = function (track) {
      observation.replacements.push({
        at: Date.now(),
        previousKind: this.track?.kind || null,
        kind: track?.kind || null,
        trackId: track?.id || null,
      });
      return nativeReplaceTrack.call(this, track);
    };
  });
}

async function firstVisible(locator, description) {
  const deadline = Date.now() + 15_000;
  while (Date.now() < deadline) {
    const count = await locator.count();
    for (let index = 0; index < count; index += 1) {
      if (await locator.nth(index).isVisible()) return locator.nth(index);
    }
    await delay(100);
  }
  throw new Error(`${description} is not visible`);
}

async function login(page, participant, password) {
  await page.goto(`${baseURL}/login`, { waitUntil: "domcontentloaded" });
  await fs.writeFile(path.join(logDir, `${participant}-login.html`), await page.content());
  await page.locator('input[name="user"]').fill(participant);
  await page.locator('input[name="password"]').fill(password);
  await Promise.all([
    page.waitForURL((url) => !url.pathname.includes("/login"), { timeout }),
    page.locator('button[type="submit"]').click(),
  ]);
  await page.waitForFunction((expected) => (
    globalThis.OC?.getCurrentUser?.()?.uid === expected
  ), participant, { timeout });
  const userId = await page.evaluate(() => globalThis.OC?.getCurrentUser?.()?.uid || "");
  assert(userId === participant, `${participant}: Nextcloud session belongs to ${userId || "nobody"}`);
  await fs.writeFile(path.join(logDir, `${participant}-after-login.html`), await page.content());
}

// captureStorageKeys reports what the payload left in this browser, other than
// its own delivery bookkeeping. Cassini records no answer from a participant,
// so this must stay empty: a key here would be a consent-shaped surface growing
// back on the quiet.
//
// cassini.sourceCapture.uploadAttempts is excluded deliberately. It counts
// refusals per buffered capture so a permanently-failing deployment stops
// re-offering a meeting-sized body forever; it says nothing about a person, and
// a run that hit one transient 502 would otherwise fail here for the wrong
// reason.
const DELIVERY_BOOKKEEPING_KEY = "cassini.sourceCapture.uploadAttempts";

async function captureStorageKeys(page) {
  return page.evaluate((bookkeeping) => (
    Object.keys(localStorage).filter((key) => key.startsWith("cassini") && key !== bookkeeping)
  ), DELIVERY_BOOKKEEPING_KEY);
}

async function mediaSnapshot(page) {
  return page.evaluate(async () => {
    const observation = globalThis.__cassiniBrowserCallE2E;
    const connections = [];
    let audioBytesSent = 0;
    let liveAudioSenders = 0;
    for (const pc of observation?.peerConnections || []) {
      const senders = pc.getSenders();
      const connectionLiveAudioSenders = senders
        .filter((sender) => sender.track?.kind === "audio" && sender.track.readyState === "live").length;
      const stats = await pc.getStats();
      let connectionAudioBytesSent = 0;
      for (const report of stats.values()) {
        if (report.type === "outbound-rtp" && !report.isRemote && (report.kind === "audio" || report.mediaType === "audio")) {
          connectionAudioBytesSent += Number(report.bytesSent || 0);
        }
      }
      if (pc.connectionState === "connected") {
        audioBytesSent += connectionAudioBytesSent;
        liveAudioSenders += connectionLiveAudioSenders;
      }
      connections.push({
        connectionState: pc.connectionState,
        iceConnectionState: pc.iceConnectionState,
        audioBytesSent: connectionAudioBytesSent,
        liveAudioSenders: connectionLiveAudioSenders,
      });
    }
    return { audioBytesSent, liveAudioSenders, connections };
  });
}

async function waitForAudioFlow(page, participant, minimumBytes = 2_000) {
  const deadline = Date.now() + timeout;
  let snapshot = { audioBytesSent: 0, liveAudioSenders: 0, connections: [] };
  while (Date.now() < deadline) {
    snapshot = await mediaSnapshot(page);
    const flowing = snapshot.connections.some((connection) => (
      connection.connectionState === "connected"
      && (connection.iceConnectionState === "connected" || connection.iceConnectionState === "completed")
      && connection.liveAudioSenders > 0
      && connection.audioBytesSent > minimumBytes
    ));
    if (flowing) return snapshot;
    await delay(100);
  }
  throw new Error(`${participant}: no connected outgoing audio sender reached ${minimumBytes} bytes: ${JSON.stringify(snapshot)}`);
}

async function persistObservedUploadBodies(page, participant, evidence, minimumResponses = 0) {
  const deadline = Date.now() + 15_000;
  let observed = [];
  while (Date.now() < deadline) {
    observed = await page.evaluate(() => (
      globalThis.__cassiniBrowserCallE2E?.uploadResponses.map((entry) => ({ ...entry })) || []
    ));
    const complete = observed.length >= minimumResponses
      && evidence.uploadResponses.length === observed.length
      && observed.every((entry) => entry.body !== null || entry.error !== null);
    if (complete) break;
    await delay(50);
  }
  assert(observed.length >= minimumResponses,
    `${participant}: observed only ${observed.length} upload response(s), wanted ${minimumResponses}`);
  assert(evidence.uploadResponses.length === observed.length,
    `${participant}: Playwright saw ${evidence.uploadResponses.length} upload response(s), page saw ${observed.length}`);
  for (let index = 0; index < observed.length; index += 1) {
    const pageResponse = observed[index];
    const networkResponse = evidence.uploadResponses[index];
    assert(pageResponse.error === null, `${participant}: upload response clone failed: ${pageResponse.error}`);
    assert(pageResponse.body !== null, `${participant}: upload response body did not finish`);
    assert(pageResponse.status === networkResponse.status && pageResponse.url === networkResponse.url,
      `${participant}: page/network upload response mismatch at index ${index}`);
    await fs.writeFile(path.join(logDir, networkResponse.bodyFile), pageResponse.body);
    networkResponse.body = pageResponse.body;
  }
  return observed;
}

async function joinCall(page, participant) {
  await page.goto(`${baseURL}/index.php/call/${roomToken}`, { waitUntil: "domcontentloaded" });
  await page.waitForFunction(() => RTCPeerConnection.__cassiniPatched === true, null, { timeout });
  const initialJoin = await firstVisible(
    page.locator("button.join-call:not([disabled])"),
    `${participant}: enabled Talk join button`,
  );
  await initialJoin.click();

  await page.locator(".media-settings").first().waitFor({ state: "visible", timeout: 15_000 });
  // Audio is the subject of the test. Turning the fake camera off keeps the
  // short real-SFU call cheap without bypassing Talk's real device dialog.
  const disableVideo = page.getByRole("button", { name: "Disable video", exact: true });
  if (await disableVideo.count() && await disableVideo.first().isVisible()) await disableVideo.first().click();
  const dialogJoin = await firstVisible(page.locator(".media-settings button.join-call"), `${participant}: media-dialog join button`);
  await dialogJoin.click();
  await page.locator(".leave-call").first().waitFor({ state: "visible", timeout });
  const media = await waitForAudioFlow(page, participant);
  return { ...media, mediaDialogClicked: true };
}

async function opfsSnapshot(page) {
  return page.evaluate(async () => {
    const root = await navigator.storage.getDirectory();
    const captures = [];
    for await (const [dirName, handle] of root.entries()) {
      if (handle.kind !== "directory" || !dirName.startsWith("capture-")) continue;
      const files = [];
      for await (const [name, child] of handle.entries()) {
        let size = null;
        if (child.kind === "file") {
          try { size = (await child.getFile()).size; } catch { /* an active sync handle can hide its current size */ }
        }
        files.push({ name, kind: child.kind, size });
      }
      captures.push({ dirName, files });
    }
    return captures;
  });
}

// waitForDrainedCaptureStorage polls until the browser has let go of its
// buffer. The payload deletes the OPFS directory only after the server accepts
// the upload, several awaits after the response this process saw, so reading
// once would race that teardown rather than test it.
async function waitForDrainedCaptureStorage(page, participant) {
  const deadline = Date.now() + 20_000;
  let captures = await opfsSnapshot(page);
  while (Date.now() < deadline && captures.length > 0) {
    await delay(250);
    captures = await opfsSnapshot(page);
  }
  assert(captures.length === 0,
    `${participant}: source-capture OPFS state survived an accepted upload: ${JSON.stringify(captures)}`);
  return captures;
}

async function talkRequest(page, method, endpoint, body) {
  return page.evaluate(async ({ requestMethod, requestEndpoint, requestBody }) => {
    const headers = { Accept: "application/json", "OCS-APIRequest": "true", requesttoken: globalThis.OC?.requestToken || "" };
    if (requestBody !== undefined) headers["Content-Type"] = "application/json";
    const response = await fetch(requestEndpoint, {
      method: requestMethod,
      credentials: "same-origin",
      headers,
      body: requestBody === undefined ? undefined : JSON.stringify(requestBody),
    });
    return { status: response.status, body: await response.text() };
  }, { requestMethod: method, requestEndpoint: endpoint, requestBody: body });
}

async function startRecording(page) {
  // Talk's AUDIO status exercises the same authoritative recording lifecycle
  // while avoiding an unnecessary composed-video workload in this audio leg.
  const start = await talkRequest(page, "POST", `/ocs/v2.php/apps/spreed/api/v1/recording/${roomToken}?format=json`, { status: 2 });
  await fs.writeFile(path.join(logDir, "recording-start.body"), start.body);
  await writeJSON("recording-start.json", start);
  assert(start.status >= 200 && start.status < 300, `Talk recording start returned HTTP ${start.status}: ${start.body}`);

  const deadline = Date.now() + timeout;
  let poll = 0;
  while (Date.now() < deadline) {
    const status = await talkRequest(page, "GET", `/ocs/v2.php/apps/spreed/api/v4/room/${roomToken}?format=json`);
    await fs.writeFile(path.join(logDir, `recording-status-${String(poll).padStart(3, "0")}.body`), status.body);
    let parsed;
    try { parsed = JSON.parse(status.body); } catch { parsed = null; }
    const callRecording = Number(parsed?.ocs?.data?.callRecording);
    if (status.status === 200 && callRecording === 2) return { callRecording, polls: poll + 1 };
    poll += 1;
    await delay(750);
  }
  throw new Error("Talk recording never became active");
}

async function replacementCount(page) {
  return page.evaluate(() => globalThis.__cassiniBrowserCallE2E?.replacements.length || 0);
}

async function waitForReplacement(page, baseline, kind, replacementTimeout = 12_000) {
  await page.waitForFunction(({ from, wantedKind }) => (
    globalThis.__cassiniBrowserCallE2E?.replacements.slice(from).some((entry) => entry.kind === wantedKind)
  ), { from: baseline, wantedKind: kind }, { timeout: replacementTimeout });
}

async function audioDeviceSnapshot(page) {
  return page.evaluate(async () => {
    const observation = globalThis.__cassiniBrowserCallE2E;
    const sender = (observation?.peerConnections || [])
      .filter((pc) => pc.connectionState === "connected")
      .flatMap((pc) => pc.getSenders())
      .find((candidate) => candidate.track?.kind === "audio" && candidate.track.readyState === "live");
    const devices = (await navigator.mediaDevices.enumerateDevices())
      .filter((device) => device.kind === "audioinput")
      .map((device) => ({ deviceId: device.deviceId, label: device.label }));
    return {
      trackId: sender?.track?.id || null,
      deviceId: sender?.track?.getSettings?.().deviceId || null,
      devices,
    };
  });
}

async function selectAudioAction(page, label) {
  const actions = page.locator(".audio-selector__action");
  let menuOpen = false;
  for (let index = 0; index < await actions.count(); index += 1) {
    if (await actions.nth(index).isVisible()) menuOpen = true;
  }
  if (!menuOpen) {
    let toggle = page.locator(".audio-selector-button .action-item__menutoggle");
    if (!await toggle.count()) toggle = page.locator(".audio-selector-button button");
    toggle = await firstVisible(toggle, "Talk microphone selector");
    await toggle.click();
  }
  await firstVisible(actions, "Talk microphone menu action");
  const count = await actions.count();
  for (let index = 0; index < count; index += 1) {
    const action = actions.nth(index);
    if (!await action.isVisible()) continue;
    const text = (await action.innerText()).trim();
    const title = (await action.getAttribute("title") || "").trim();
    if (text === label || title === label) {
      await action.click();
      return;
    }
  }
  throw new Error(`Talk microphone menu has no ${JSON.stringify(label)} action`);
}

async function switchMicrophone(page) {
  const before = await audioDeviceSnapshot(page);
  const alternatives = before.devices.filter((device) => (
    device.deviceId && device.deviceId !== before.deviceId && device.label
  ));
  assert(alternatives.length > 0, `Chromium exposed no second microphone: ${JSON.stringify(before.devices)}`);

  const attempts = [];
  for (const candidate of alternatives) {
    const baseline = await replacementCount(page);
    try {
      await selectAudioAction(page, candidate.label);
      await waitForReplacement(page, baseline, "audio");
      const deadline = Date.now() + 8_000;
      let after = await audioDeviceSnapshot(page);
      while (Date.now() < deadline && (
        !after.trackId || after.trackId === before.trackId
        || !after.deviceId || after.deviceId === before.deviceId
      )) {
        await delay(100);
        after = await audioDeviceSnapshot(page);
      }
      attempts.push({ selected: candidate.label, after });
      if (after.trackId && after.trackId !== before.trackId
        && after.deviceId && after.deviceId !== before.deviceId) {
        return { mode: "distinct-device", selected: candidate.label, before, after, attempts };
      }
    } catch (error) {
      attempts.push({ selected: candidate.label, error: error.message });
    }
  }
  throw new Error(`Talk did not switch to a distinct microphone: ${JSON.stringify({ before, attempts })}`);
}

async function leaveCall(page, participant, required = true) {
  const leaveRoot = page.locator(".leave-call");
  let leaveVisible = false;
  for (let index = 0; index < await leaveRoot.count(); index += 1) {
    if (await leaveRoot.nth(index).isVisible()) leaveVisible = true;
  }
  if (!leaveVisible) {
    if (required) throw new Error(`${participant}: no visible Talk leave control`);
    return;
  }

  const direct = page.locator("button.leave-call");
  if (await direct.count() && await direct.first().isVisible()) {
    await direct.first().click();
    await leaveRoot.first().waitFor({ state: "hidden", timeout: 15_000 });
    return;
  }
  const splitAction = page.locator(".leave-call-button--split");
  if (!await splitAction.count() || !await splitAction.first().isVisible()) {
    const splitToggle = await firstVisible(
      page.locator(".leave-call-actions--split .action-item__menutoggle"),
      `${participant}: leave-call menu`,
    );
    await splitToggle.click();
  }
  await (await firstVisible(splitAction, `${participant}: leave-call split action`)).click();
  await leaveRoot.first().waitFor({ state: "hidden", timeout: 15_000 });
}

let browser;
let aliceContext;
let bobContext;
let alicePage;
let bobPage;
let aliceLeft = false;
let bobLeft = false;
try {
  browser = await chromium.launch({
    headless: true,
    args: [
      "--use-fake-device-for-media-stream",
      "--use-fake-ui-for-media-stream",
      "--autoplay-policy=no-user-gesture-required",
      "--mute-audio",
    ],
  });
  // Talk rejects Chromium's automation-only "HeadlessChrome" product token
  // before media setup. Keep the real engine version while presenting the
  // normal Linux Chrome token that this same browser uses in headed mode.
  result.chromiumVersion = browser.version();
  result.contextUserAgent = `Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/${result.chromiumVersion} Safari/537.36`;
  const contextOptions = {
    locale: "en-US",
    viewport: { width: 1440, height: 1000 },
    userAgent: result.contextUserAgent,
  };
  aliceContext = await browser.newContext(contextOptions);
  bobContext = await browser.newContext(contextOptions);
  await installObservation(aliceContext);
  await installObservation(bobContext);
  alicePage = await aliceContext.newPage();
  bobPage = await bobContext.newPage();
  const aliceUploadEvidence = attachEvidence(alicePage, "alice");
  const bobUploadEvidence = attachEvidence(bobPage, "bob");

  await login(alicePage, "alice", process.env.ALICE_PASSWORD);
  await login(bobPage, "bob", process.env.BOB_PASSWORD);

  result.alice.preRecordingOPFS = await opfsSnapshot(alicePage);
  result.bob.preRecordingOPFS = await opfsSnapshot(bobPage);
  assert(result.alice.preRecordingOPFS.length === 0, "Alice had capture storage before joining/recording");
  assert(result.bob.preRecordingOPFS.length === 0, "Bob had capture storage before joining/recording");

  result.alice.mediaBeforeRecording = await joinCall(alicePage, "alice");
  result.bob.mediaBeforeRecording = await joinCall(bobPage, "bob");
  await delay(2_200);
  result.alice.joinedBeforeRecordingOPFS = await opfsSnapshot(alicePage);
  result.bob.joinedBeforeRecordingOPFS = await opfsSnapshot(bobPage);
  assert(result.alice.joinedBeforeRecordingOPFS.length === 0, "Alice captured before Talk recording became active");
  assert(result.bob.joinedBeforeRecordingOPFS.length === 0, "Bob captured before Talk recording became active");
  result.recording = await startRecording(alicePage);

  // Both browsers, not only the one that started the recording. Talk's
  // confirmed recording is the whole trigger, so a Bob who records nothing here
  // means the payload is still gating on something of its own.
  const captureStarted = async () => {
    const root = await navigator.storage.getDirectory();
    for await (const [name] of root.entries()) if (name.startsWith("capture-")) return true;
    return false;
  };
  await alicePage.waitForFunction(captureStarted, null, { timeout });
  await bobPage.waitForFunction(captureStarted, null, { timeout });
  await delay(3_200);
  result.bob.duringRecordingOPFS = await opfsSnapshot(bobPage);
  assert(result.bob.duringRecordingOPFS.length === 1,
    `Bob buffered ${result.bob.duringRecordingOPFS.length} captures for one recorded call`);
  const bobSegmentFiles = result.bob.duringRecordingOPFS.flatMap((capture) => capture.files)
    .filter((file) => /^segment-\d+\.webm$/.test(file.name));
  assert(bobSegmentFiles.length >= 1, "Bob created no OPFS segment file during the recorded call");

  result.alice.mediaBeforeSwitch = await mediaSnapshot(alicePage);
  result.alice.microphoneSwitch = await switchMicrophone(alicePage);
  result.alice.mediaImmediatelyAfterSwitch = await mediaSnapshot(alicePage);
  const bytesImmediatelyAfterSwitch = result.alice.mediaImmediatelyAfterSwitch.audioBytesSent;
  await delay(3_200);
  result.alice.mediaAfterSwitch = await waitForAudioFlow(alicePage, "alice", bytesImmediatelyAfterSwitch + 2_000);
  result.alice.duringRecordingOPFS = await opfsSnapshot(alicePage);
  const segmentFiles = result.alice.duringRecordingOPFS.flatMap((capture) => capture.files)
    .filter((file) => /^segment-\d+\.webm$/.test(file.name));
  assert(segmentFiles.length >= 2, `microphone switch left only ${segmentFiles.length} OPFS segment file(s)`);
  assert(segmentFiles.some((file) => Number(file.size || 0) > 1_000), "sealed pre-switch OPFS segment is implausibly small");

  const captureUpload = (response) => (
    response.request().method() === "POST" && response.url().includes("/operator/capture/upload")
  );

  // Both listeners are armed before either participant leaves. Bob's upload is
  // normally triggered by his own leave, but if this Talk stops the recording
  // when the moderator who started it goes, Talk's confirmed recording-off
  // uploads Bob's capture at that moment instead — and a listener armed after
  // Alice left would wait out its timeout on an upload that already happened.
  const aliceUploadResponse = alicePage.waitForResponse(captureUpload, { timeout: 45_000 });
  const bobUploadResponse = bobPage.waitForResponse(captureUpload, { timeout: 90_000 });

  await leaveCall(alicePage, "alice");
  aliceLeft = true;
  const aliceUpload = await aliceUploadResponse;
  const observedAliceUploads = await persistObservedUploadBodies(alicePage, "alice", aliceUploadEvidence, 1);
  result.alice.upload = observedAliceUploads.at(-1);
  assert(result.alice.upload.status === aliceUpload.status(), "alice: page and Playwright disagreed on upload HTTP status");
  assert(aliceUpload.status() === 202, `alice: source capture upload returned HTTP ${aliceUpload.status()}: ${result.alice.upload.body}`);

  await leaveCall(bobPage, "bob");
  bobLeft = true;
  const bobUpload = await bobUploadResponse;
  const observedBobUploads = await persistObservedUploadBodies(bobPage, "bob", bobUploadEvidence, 1);
  result.bob.upload = observedBobUploads.at(-1);
  assert(result.bob.upload.status === bobUpload.status(), "bob: page and Playwright disagreed on upload HTTP status");
  assert(bobUpload.status() === 202, `bob: source capture upload returned HTTP ${bobUpload.status()}: ${result.bob.upload.body}`);

  result.alice.afterLeaveOPFS = await waitForDrainedCaptureStorage(alicePage, "alice");
  result.bob.afterLeaveOPFS = await waitForDrainedCaptureStorage(bobPage, "bob");
  // Nothing per participant is stored anywhere in either browser: capture
  // followed Talk's recording and left no trace of its own behind.
  result.alice.captureStorageKeys = await captureStorageKeys(alicePage);
  result.bob.captureStorageKeys = await captureStorageKeys(bobPage);
  assert(result.alice.captureStorageKeys.length === 0,
    `alice: browser storage holds capture keys ${JSON.stringify(result.alice.captureStorageKeys)}`);
  assert(result.bob.captureStorageKeys.length === 0,
    `bob: browser storage holds capture keys ${JSON.stringify(result.bob.captureStorageKeys)}`);
  await alicePage.screenshot({ path: path.join(logDir, "alice-after-leave.png"), fullPage: true }).catch(() => {});
  await bobPage.screenshot({ path: path.join(logDir, "bob-after-leave.png"), fullPage: true }).catch(() => {});
  result.alice.observedUploadRequestCount = aliceUploadEvidence.uploadRequests.length;
  result.alice.observedUploadResponseCount = aliceUploadEvidence.uploadResponses.length;
  result.bob.observedUploadRequestCount = bobUploadEvidence.uploadRequests.length;
  result.bob.observedUploadResponseCount = bobUploadEvidence.uploadResponses.length;
  await drainEvidence();
  assert(evidenceErrors.length === 0, `could not persist browser evidence: ${evidenceErrors.join("; ")}`);
  result.result = "passed";
} catch (error) {
  result.error = { message: error.message, stack: error.stack };
  process.exitCode = 1;
} finally {
  if (alicePage && !aliceLeft) await leaveCall(alicePage, "alice", false).catch(() => {});
  if (bobPage && !bobLeft) await leaveCall(bobPage, "bob", false).catch(() => {});
  if (alicePage) await alicePage.screenshot({ path: path.join(logDir, "alice-final.png"), fullPage: true }).catch(() => {});
  if (bobPage) await bobPage.screenshot({ path: path.join(logDir, "bob-final.png"), fullPage: true }).catch(() => {});
  await drainEvidence();
  await browser?.close().catch(() => {});
  await drainEvidence();
  if (evidenceErrors.length > 0 && result.result === "passed") {
    result.result = "failed";
    result.error = { message: `could not persist browser evidence: ${evidenceErrors.join("; ")}` };
    process.exitCode = 1;
  }
  result.finishedAt = new Date().toISOString();
  await writeJSON(path.basename(resultPath), result);
  process.stdout.write(`browser-call-capture result=${result.result} evidence=${resultPath}\n`);
}
