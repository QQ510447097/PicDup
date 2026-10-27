// Minimal CDP screenshot/interaction helper for local UI verification.
// Usage: node tools/cdp_shot.mjs --url URL --out FILE [--width 1440] [--height 1000]
//        [--script "expr"] [--wait 800] [--port 9223]
import fs from 'node:fs';

const args = process.argv.slice(2);
const opt = { width: 1440, height: 1000, port: 9223, wait: 1000, scripts: [], existing: false, viewport: true, shot: true };
for (let i = 0; i < args.length; i++) {
  const a = args[i];
  if (a === '--url') opt.url = args[++i];
  else if (a === '--out') opt.out = args[++i];
  else if (a === '--width') opt.width = Number(args[++i]);
  else if (a === '--height') opt.height = Number(args[++i]);
  else if (a === '--port') opt.port = Number(args[++i]);
  else if (a === '--wait') opt.wait = Number(args[++i]);
  else if (a === '--existing') opt.existing = true;
  else if (a === '--no-viewport') opt.viewport = false;
  else if (a === '--no-shot') opt.shot = false;
  else if (a === '--script') opt.scripts.push(args[++i]);
}
if (!opt.out || (!opt.url && !opt.existing)) {
  console.error('missing --url/--existing or --out');
  process.exit(2);
}

const base = `http://127.0.0.1:${opt.port}`;
let target;
if (opt.existing) {
  const list = await (await fetch(`${base}/json/list`)).json();
  target = list.find((t) => t.type === 'page');
  if (!target) throw new Error('no existing page target');
  opt.url = target.url;
} else {
  const targetResp = await fetch(`${base}/json/new?about:blank`, { method: 'PUT' });
  if (!targetResp.ok) throw new Error(`cannot create target: ${targetResp.status}`);
  target = await targetResp.json();
}
const ws = new WebSocket(target.webSocketDebuggerUrl);
await new Promise((resolve, reject) => {
  ws.addEventListener('open', resolve, { once: true });
  ws.addEventListener('error', reject, { once: true });
});

let id = 0;
const pending = new Map();
const events = [];
const pageErrors = [];
ws.addEventListener('message', (ev) => {
  const msg = JSON.parse(ev.data);
  if (msg.id && pending.has(msg.id)) {
    const { resolve, reject } = pending.get(msg.id);
    pending.delete(msg.id);
    if (msg.error) reject(new Error(JSON.stringify(msg.error)));
    else resolve(msg.result);
  } else if (msg.method) {
    if (msg.method === 'Runtime.exceptionThrown') {
      pageErrors.push(msg.params?.exceptionDetails?.text || 'exception');
    } else if (msg.method === 'Log.entryAdded' && msg.params?.entry?.level === 'error') {
      pageErrors.push(msg.params.entry.text || 'console error');
    }
    events.push(msg);
  }
});
function send(method, params = {}) {
  const msgId = ++id;
  ws.send(JSON.stringify({ id: msgId, method, params }));
  return new Promise((resolve, reject) => pending.set(msgId, { resolve, reject }));
}
async function waitEvent(method, timeout = 10000) {
  const start = Date.now();
  while (Date.now() - start < timeout) {
    const idx = events.findIndex((e) => e.method === method);
    if (idx >= 0) return events.splice(idx, 1)[0];
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error(`timeout waiting for ${method}`);
}

await send('Page.enable');
await send('Runtime.enable');
await send('Log.enable');
if (opt.viewport) {
  await send('Emulation.setDeviceMetricsOverride', {
    width: opt.width, height: opt.height, deviceScaleFactor: 1, mobile: false,
  });
}
events.length = 0;
if (!opt.existing) {
  await send('Page.navigate', { url: opt.url });
  await waitEvent('Page.loadEventFired', 15000);
  await new Promise((r) => setTimeout(r, Math.max(500, opt.wait)));
}

for (const script of opt.scripts) {
  try {
    const result = await send('Runtime.evaluate', {
      expression: `(async () => { ${script} })()`,
      awaitPromise: true,
      returnByValue: true,
    });
    if (result.exceptionDetails) {
      console.error('script error:', JSON.stringify(result.exceptionDetails));
    } else if (result.result?.value !== undefined) {
      console.log('script result:', JSON.stringify(result.result.value));
    }
  } catch (err) {
    console.error('script failed:', err.message);
  }
  await new Promise((r) => setTimeout(r, 500));
}
await new Promise((r) => setTimeout(r, 300));

if (opt.shot) {
  const shot = await send('Page.captureScreenshot', { format: 'png', fromSurface: true });
  fs.writeFileSync(opt.out, Buffer.from(shot.data, 'base64'));
  console.log(`wrote ${opt.out}`);
} else {
  console.log('screenshot skipped');
}
if (pageErrors.length) console.log('page errors:', JSON.stringify(pageErrors.slice(0, 8)));
if (!opt.existing) await send('Page.close').catch(() => {});
ws.close();
