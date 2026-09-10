const token = location.hash.slice(1);
const $ = selector => document.querySelector(selector);
let busy = false;
async function api(path, body) {
  const response = await fetch(path, {method: body ? 'POST' : 'GET', headers: {Authorization: `Bearer ${token}`, ...(body ? {'Content-Type': 'application/json'} : {})}, ...(body ? {body: JSON.stringify(body)} : {})});
  const result = await response.json();
  if (!response.ok) throw new Error(result.error || `HTTP ${response.status}`);
  return result;
}
function render(state) {
  const c = state.conditions;
  $('#status').textContent = c.label;
  $('.status').style.color = c.offline ? '#ae2535' : c.label === 'Normal' ? '#11714a' : '#996500';
  $('#summary').textContent = `${c.scope === 'uploads' ? 'Recording uploads only' : 'Test tabs and WebRTC'} · ${c.latency} ms latency · ↓ ${c.download ? c.download + ' kbit/s' : 'unlimited'} · ↑ ${c.upload ? c.upload + ' kbit/s' : 'unlimited'} · ${c.loss}% media loss`;
  $('#profile').textContent = state.profile;
  $('#tabs').replaceChildren(...state.tabs.map(tab => {const li = document.createElement('li');li.textContent = tab.url;return li;}));
  $('#events').replaceChildren(...state.events.map(event => {const li = document.createElement('li'), time = document.createElement('time'), text = document.createElement('span');time.textContent = new Date(event.at).toLocaleTimeString();text.textContent = event.message;li.append(time,text);return li;}));
  for (const button of document.querySelectorAll('[data-preset]')) button.dataset.active = button.querySelector('strong').textContent === c.label ? 'true' : 'false';
}
async function change(input) {
  if (busy) return;busy = true;
  $('#error').textContent = '';
  for (const button of document.querySelectorAll('button')) button.disabled = true;
  try {render(await api('/conditions', input));} catch (error) {$('#error').textContent = error.message;}
  finally {busy = false;for (const button of document.querySelectorAll('button')) button.disabled = false;}
}
for (const button of document.querySelectorAll('[data-preset]')) button.addEventListener('click', () => change({preset: button.dataset.preset}));
$('#restore').addEventListener('click', () => change({preset: 'normal'}));
$('#custom').addEventListener('submit', event => {event.preventDefault();const form = new FormData(event.currentTarget);change({latency: +form.get('latency'),download: +form.get('download'),upload: +form.get('upload'),loss: +form.get('loss'),scope: form.get('scope'),offline: false});});
async function refresh() {if (!busy) {try {render(await api('/state'));} catch (error) {$('#error').textContent = `Controls unavailable: ${error.message}`;}}}
void refresh();setInterval(refresh, 1500);
