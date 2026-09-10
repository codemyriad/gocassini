#!/usr/bin/env node
import {chromium} from '@playwright/test';
import {createServer} from 'node:http';
import {randomBytes} from 'node:crypto';
import {mkdir, mkdtemp, readFile, writeFile, appendFile} from 'node:fs/promises';
import {resolve, dirname} from 'node:path';
import {fileURLToPath, pathToFileURL} from 'node:url';
import {PRESETS, validateConditions, applyConditions} from '../network-lab/network.mjs';

const repo = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
export async function startLab({url = 'http://127.0.0.1:28080/', port = 28181, profile, headless = false} = {}) {
  if (!['http:', 'https:', 'about:'].includes(new URL(url).protocol)) throw new Error('Expected an HTTP(S) URL');
  const runtime = resolve(repo, 'harness/runtime/network-lab');
  await mkdir(runtime, {recursive: true, mode: 0o700});
  profile = profile ? resolve(profile) : await mkdtemp(resolve(runtime, 'chrome-'));
  await mkdir(profile, {recursive: true, mode: 0o700});
  const token = randomBytes(24).toString('hex');
  let conditions = {...PRESETS.normal}, closed = false, queue = Promise.resolve(), context, controlPage;
  const sessions = new Map(), events = [];
  let origin;
  const logPath = resolve(profile, 'network-events.jsonl');
  function event(message) {
    const entry = {at: new Date().toISOString(), message};
    events.unshift(entry); events.length = Math.min(events.length, 80);
    void appendFile(logPath, JSON.stringify(entry) + '\n').catch(() => {});
  }
  const state = () => ({conditions, profile, events, tabs: [...sessions.keys()].map(page => ({url: page.url()}))});
  async function change(next) {
    // Serialize changes so a rapid restore cannot be overtaken by a slow preset.
    const operation = queue.then(async () => {
      const before = conditions;
      conditions = next;
      try {
        await Promise.all([...sessions.values()].map(async pending => applyConditions(await pending, next)));
        event(`Network: ${next.label}; ${next.scope}; latency ${next.latency} ms; down ${next.download || 'unlimited'} / up ${next.upload || 'unlimited'} kbit/s; loss ${next.loss}%`);
      } catch (error) {
        conditions = before;
        await Promise.allSettled([...sessions.values()].map(async pending => applyConditions(await pending, before)));
        throw error;
      }
    });
    queue = operation.catch(() => {});
    return operation;
  }
  async function attach(page) {
    if (page === controlPage || sessions.has(page)) return sessions.get(page);
    const pending = (async () => {
      const session = await context.newCDPSession(page);
      await session.send('Network.enable');
      await applyConditions(session, conditions);
      return session;
    })();
    sessions.set(page, pending);
    page.on('close', () => sessions.delete(page));
    page.on('request', request => {
      if (/\/operator\/capture\/(upload|register)$/.test(new URL(request.url()).pathname)) event(`${request.method()} ${new URL(request.url()).pathname.endsWith('/upload') ? 'Recording upload started' : 'Capture announcement'}`);
    });
    page.on('response', response => {
      if (/\/operator\/capture\/(upload|register)$/.test(new URL(response.url()).pathname)) event(`${new URL(response.url()).pathname.endsWith('/upload') ? 'Recording upload response' : 'Capture announcement response'}: HTTP ${response.status()}`);
    });
    page.on('requestfailed', request => {
      if (request.url().includes('/operator/capture/')) event(`Capture request failed: ${request.failure()?.errorText}`);
    });
    return pending;
  }
  const server = createServer(async (req, res) => {
    res.setHeader('Cache-Control', 'no-store');
    const send = (status, value) => {res.writeHead(status, {'Content-Type': 'application/json'}); res.end(JSON.stringify(value));};
    try {
      if (req.headers.host !== new URL(origin).host) return send(403, {error: 'Invalid host'});
      const path = new URL(req.url, origin).pathname;
      if (req.method === 'GET' && path === '/') {
        res.writeHead(200, {'Content-Type': 'text/html; charset=utf-8', 'Content-Security-Policy': "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors 'none'"});
        return res.end(await readFile(resolve(repo, 'harness/network-lab/controls.html')));
      }
      if (req.method === 'GET' && path === '/controls.js') {
        res.writeHead(200, {'Content-Type': 'text/javascript'});
        return res.end(await readFile(resolve(repo, 'harness/network-lab/controls.js')));
      }
      if (req.headers.authorization !== `Bearer ${token}`) return send(403, {error: 'Open the control URL printed by the launcher'});
      if (req.method === 'GET' && path === '/state') return send(200, state());
      if (req.method !== 'POST' || path !== '/conditions') return send(404, {error: 'Not found'});
      if (req.headers.origin && req.headers.origin !== origin) return send(403, {error: 'Invalid origin'});
      let raw = '';for await (const chunk of req) {raw += chunk; if (raw.length > 4096) return send(413, {error: 'Request too large'});}
      const input = JSON.parse(raw);
      const next = input.preset ? (Object.hasOwn(PRESETS, input.preset) ? PRESETS[input.preset] : null) : validateConditions(input);
      if (!next) return send(400, {error: 'Unknown preset'});
      await change({...next});
      send(200, state());
    } catch (error) {event(`Control error: ${error.message}`); send(400, {error: error.message});}
  });
  await new Promise((resolve, reject) => {server.once('error', reject); server.listen(port, '127.0.0.1', resolve);});
  origin = `http://127.0.0.1:${server.address().port}`;
  async function close() {
    if (closed) return; closed = true;
    await context?.close().catch(() => {});
    server.closeAllConnections();
    await new Promise(resolve => server.close(resolve));
    event('Browser closed. Profile retained for recovery of buffered audio.');
  }
  try {
    context = await chromium.launchPersistentContext(profile, {
      channel: 'chrome', headless, viewport: null,
      // A real microphone, ordinary permissions and audible calls.
      ignoreDefaultArgs: ['--mute-audio'],
      args: ['--no-first-run', '--no-default-browser-check'],
    });
    const testPage = context.pages()[0] ?? await context.newPage();
    // Establish the network profile before navigation and WebRTC socket creation.
    await attach(testPage);
    controlPage = await context.newPage();
    context.on('page', page => {void attach(page).catch(error => event(`Could not control new tab: ${error.message}`));});
    context.on('close', () => {void close();});
    const controlURL = `${origin}/#${token}`;
    await controlPage.goto(controlURL);
    await testPage.goto(url, {waitUntil: 'domcontentloaded', timeout: 30000}).catch(error => event(`Navigation: ${error.message}`));
    await controlPage.bringToFront();
    await writeFile(resolve(runtime, 'latest.json'), JSON.stringify({pid: process.pid, controlURL, profile, url}, null, 2), {mode: 0o600});
    event('Ready. Normal network. Control tab is never throttled.');
    return {context, testPage, controlPage, origin, token, profile, state, change, close};
  } catch (error) {await close(); throw error;}
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const options = {};
  for (let i = 2; i < process.argv.length; i++) {
    const arg = process.argv[i];
    if (arg === '--help') {
      console.log('Usage: node harness/bin/network-lab.mjs [--url URL] [--port PORT] [--profile DIR] [--headless]\nDefaults: local Nextcloud; control port 28181; a fresh disposable Chrome profile.\nClosing this Chrome window stops the lab. Profiles are retained for buffered-audio recovery.');
      process.exit(0);
    }
    if (arg === '--headless') options.headless = true;
    else if (['--url', '--port', '--profile'].includes(arg) && process.argv[i + 1]) options[arg.slice(2)] = arg === '--port' ? Number(process.argv[++i]) : process.argv[++i];
    else throw new Error(`Unknown or incomplete argument: ${arg}`);
  }
  const lab = await startLab(options);
  console.log(`Network controls: ${lab.origin}/#${lab.token}\nTest profile: ${lab.profile}\nClose the dedicated Chrome window or press Ctrl+C to stop.`);
  for (const signal of ['SIGINT', 'SIGTERM']) process.once(signal, () => {void lab.close();});
}
