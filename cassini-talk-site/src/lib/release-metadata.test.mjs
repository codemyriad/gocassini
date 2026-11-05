import assert from 'node:assert/strict';
import { test } from 'node:test';
import { loadReleases, parseMacRelease, parseMirroredRelease, releaseLine } from './release-metadata.mjs';

const item = (version, build, date, size) => `<item><sparkle:version>${build}</sparkle:version><sparkle:shortVersionString>${version}</sparkle:shortVersionString><pubDate>${date}</pubDate><enclosure length="${size}" /></item>`;
const latest = item('0.1.1',13,'Thu, 05 Nov 2026 05:48:24 +0000',79420154);
const old = item('0.1.0',11,'Wed, 04 Nov 2026 21:38:15 +0000',79356294);
const mirrored = platform => ({schema:1,platform,version:platform==='android'?'0.4.0':'0.3.0',published_at:'2026-11-05T15:11:17Z',primary:platform==='android'?'apk':'flatpak',assets:platform==='android'?[{kind:'apk',size:73639185}]:[{kind:'flatpak',size:45430336},{kind:'ubuntu',size:35370288}]});

test('Mac release comes from the highest build, regardless of item order', () => {
  for(const xml of [old+latest,latest+old]) {
    const release=parseMacRelease(`<rss><channel>${xml}</channel></rss>`);
    assert.equal(release.version,'0.1.1');
    assert.equal(release.size,79420154);
    assert.equal(releaseLine(release),'Version 0.1.1 · 5 Nov 2026 · 79.4 MB');
  }
});
test('invalid appcasts fail rather than supplying a stale version', () => {
  for(const xml of ['', '<rss/>',item('unknown',13,'bad',1)]) assert.throws(()=>parseMacRelease(xml));
});
test('Linux chooses native installers even if an old manifest advertises Flatpak', () => {
  assert.equal(parseMirroredRelease(mirrored('linux'),'linux').size,35370288);
  const release=mirrored('linux');release.assets=release.assets.filter(a=>a.kind==='flatpak');
  assert.throws(()=>parseMirroredRelease(release,'linux'));
});
test('the static build reads published data for all released platforms', async t => {
  const requests=[];
  t.mock.method(globalThis,'fetch',async(url,options)=>{
    requests.push([url,options]);
    const platform=url.split('/')[4];
    return {ok:true,text:async()=>`<rss>${latest}</rss>`,json:async()=>mirrored(platform)};
  });
  const releases=await loadReleases([{id:'mac',note:'Model included'}]);
  assert.match(releases.get('mac'),/^Version 0\.1\.1/);
  assert.match(releases.get('android'),/^Version 0\.4\.0/);
  assert.match(releases.get('linux'),/35\.4 MB/);
  assert.equal(requests.length,3);
  for(const [url,options] of requests){assert.ok(url.startsWith('https://cassini.talk/download/'));assert.equal(options.cache,'no-cache');}
});
test('a build with unavailable metadata fails instead of hardcoding a version', async t => {
  t.mock.method(globalThis,'fetch',async()=>({ok:false,status:503}));
  await assert.rejects(loadReleases([]),/HTTP 503/);
});
