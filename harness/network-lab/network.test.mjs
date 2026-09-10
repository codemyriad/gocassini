import test from 'node:test';
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {startLab} from '../bin/network-lab.mjs';
import {PRESETS} from './network.mjs';

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
test('Chrome isolates HTTP throttling, media loss, offline recovery and upload-only rules', {timeout: 60000}, async () => {
  const fixture = createServer(async (req, res) => {
    for await (const chunk of req) { /* drain upload */ }
    res.writeHead(200, {'Content-Type': 'text/html', 'Cache-Control': 'no-store'});res.end('<title>Network test</title>ok');
  });
  await new Promise(resolve => fixture.listen(0, '127.0.0.1', resolve));
  const url = `http://127.0.0.1:${fixture.address().port}/`;
  let lab;
  try {
    lab = await startLab({url, port: 0, headless: true});
    const {testPage: page, controlPage: controls} = lab;
    const probe = (path = '/', body) => page.evaluate(async ({path, body}) => {
      const start = performance.now();await (await fetch(path, body ? {method: 'POST',body} : {})).text();return performance.now()-start;
    }, {path, body});
    const baseline = await probe();
    await lab.change({...PRESETS.normal,latency:500,label:'Latency test'});
    const slowed = await probe();
    assert.ok(slowed > baseline + 350, `HTTP latency: baseline=${baseline} slowed=${slowed}`);
    await lab.change({...PRESETS.uploads,latency:500,upload:64});
    const ordinary = await probe();
    const upload = await probe('/index.php/apps/app_api/proxy/gocassini/operator/capture/upload','x'.repeat(16384));
    assert.ok(ordinary < 350, `Upload-only rule slowed unrelated HTTP: ${ordinary}`);
    assert.ok(upload > 1500, `Upload wasn't throttled: ${upload}`);
    await lab.change({...PRESETS.normal});
    await page.evaluate(async () => {
      window.received=0;
      const a=window.a=new RTCPeerConnection(), b=window.b=new RTCPeerConnection();
      a.onicecandidate=e=>e.candidate&&b.addIceCandidate(e.candidate);
      b.onicecandidate=e=>e.candidate&&a.addIceCandidate(e.candidate);
      b.ondatachannel=e=>e.channel.onmessage=()=>window.received++;
      const channel=window.channel=a.createDataChannel('probe',{ordered:false,maxRetransmits:0});
      const ready=new Promise(resolve=>channel.onopen=resolve);
      await a.setLocalDescription(await a.createOffer());await b.setRemoteDescription(a.localDescription);
      await b.setLocalDescription(await b.createAnswer());await a.setRemoteDescription(b.localDescription);
      await ready;
      window.timer=setInterval(()=>channel.readyState==='open'&&channel.send('packet'),20);
    });
    const packets = async () => {const before = await page.evaluate(()=>window.received);await sleep(1000);return (await page.evaluate(()=>window.received))-before;};
    const normal = await packets();assert.ok(normal > 20);
    await lab.change({...PRESETS.media,loss:100});await sleep(300);
    const lost = await packets();assert.equal(lost,0,'An existing WebRTC connection ignored packet loss');
    await lab.change({...PRESETS.normal});
    const recovered = await packets();assert.ok(recovered > 20,`Media failed to recover: ${recovered}`);
    await lab.change({...PRESETS.offline});
    assert.equal(await page.evaluate(()=>fetch('/').then(()=>false,()=>true)),true,'HTTP did not go offline');
    // The separate control tab remains reachable even when the test is offline.
    await controls.reload();
    await controls.getByRole('button',{name:'Restore normal',exact:true}).click();
    for (let i=0;i<30&&lab.state().conditions.offline;i++) await sleep(100);
    assert.equal(lab.state().conditions.offline,false);
    await probe();
    assert.equal((await fetch(`${lab.origin}/state`)).status,403);
    assert.equal((await fetch(`${lab.origin}/conditions`,{method:'POST',headers:{Authorization:`Bearer ${lab.token}`,Origin:'https://unrelated.example'},body:'{"preset":"offline"}'})).status,403);
    console.log(JSON.stringify({baseline,slowed,ordinary,upload,normal,lost,recovered}));
  } finally {await lab?.close();fixture.closeAllConnections();await new Promise(resolve=>fixture.close(resolve));}
});
