export const PRESETS = {
  normal: {label: 'Normal', latency: 0, download: 0, upload: 0, loss: 0, scope: 'all', offline: false},
  poor: {label: 'Poor connection', latency: 300, download: 512, upload: 128, loss: 10, scope: 'all', offline: false},
  bad: {label: 'Very poor connection', latency: 800, download: 128, upload: 32, loss: 30, scope: 'all', offline: false},
  media: {label: '30% media packet loss', latency: 0, download: 0, upload: 0, loss: 30, scope: 'all', offline: false},
  uploads: {label: 'Slow recording upload', latency: 200, download: 0, upload: 32, loss: 0, scope: 'uploads', offline: false},
  offline: {label: 'Disconnected', latency: 0, download: 0, upload: 0, loss: 100, scope: 'all', offline: true},
};

export function validateConditions(input) {
  const result = {};
  for (const [name, max] of [['latency', 10000], ['download', 1000000], ['upload', 1000000], ['loss', 100]]) {
    if (typeof input[name] !== 'number' || !Number.isFinite(input[name]) || input[name] < 0 || input[name] > max) throw new Error(`Invalid ${name}`);
    result[name] = input[name];
  }
  if (!['all', 'uploads'].includes(input.scope) || typeof input.offline !== 'boolean') throw new Error('Invalid scope or offline setting');
  if (input.scope === 'uploads' && (input.loss || input.offline)) throw new Error('Media loss and disconnection require the whole test-tab scope');
  return {...result, scope: input.scope, offline: input.offline, label: 'Custom'};
}

export function rulesFor(conditions) {
  const normal = {urlPattern: '', latency: 0, downloadThroughput: -1, uploadThroughput: -1, packetLoss: 0, offline: false};
  const rule = {...normal, latency: conditions.latency,
    downloadThroughput: conditions.download ? conditions.download * 1000 / 8 : -1,
    uploadThroughput: conditions.upload ? conditions.upload * 1000 / 8 : -1,
    packetLoss: conditions.offline ? 100 : conditions.loss, offline: conditions.offline};
  // The empty global pattern includes WebRTC. A URL-specific upload rule does
  // not affect media. Always retain an explicit normal global rule so sockets
  // have a throttling profile BEFORE a call creates them.
  return conditions.scope === 'uploads'
    ? [{...rule, urlPattern: '*://*:*/*/operator/capture/upload'}, normal]
    : [rule];
}

export async function applyConditions(session, conditions) {
  await session.send('Network.emulateNetworkConditionsByRule', {matchedNetworkConditions: rulesFor(conditions)});
  await session.send('Network.overrideNetworkState', {
    offline: conditions.offline, latency: conditions.latency,
    downloadThroughput: conditions.download ? conditions.download * 1000 / 8 : -1,
    uploadThroughput: conditions.upload ? conditions.upload * 1000 / 8 : -1,
    connectionType: conditions.offline ? 'none' : 'wifi',
  });
}
