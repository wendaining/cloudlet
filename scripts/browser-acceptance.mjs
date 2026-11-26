import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { once } from 'node:events';
import { mkdtemp, writeFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { isIPv4 } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const [project, bridgeAddress] = process.argv.slice(2);
const uuid = /^[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$/iu;
const port = Number(process.env.CLOUDLET_API_PORT ?? 8080);
const email = process.env.CLOUDLET_DEV_EMAIL;
const password = process.env.CLOUDLET_DEV_PASSWORD;
if (!uuid.test(project ?? '') || !isIPv4(bridgeAddress ?? '') || !email || !password ||
    !Number.isInteger(port) || port < 1 || port > 65535 || process.argv.length !== 4) {
  throw new Error('用法：node --env-file=.env.local scripts/browser-acceptance.mjs <项目 UUID> <本机 Docker 网桥 IPv4>');
}

const origin = `http://127.0.0.1:${port}`;
const sessions = `/api/v1/projects/${project}/browser-sessions`;
const marker = 'Cloudlet Browser acceptance click';
let cookie;
async function request(method, path, status, payload) {
  const headers = new Headers();
  if (cookie) headers.set('Cookie', cookie);
  if (payload !== undefined) headers.set('Content-Type', 'application/json');
  const response = await fetch(origin + path, {
    method, headers, redirect: 'error', signal: AbortSignal.timeout(60000),
    body: payload === undefined ? undefined : JSON.stringify(payload),
  });
  if (response.status !== status) throw new Error(`${method} ${path} 返回 HTTP ${response.status}，预期 ${status}`);
  return response;
}

const fixture = createServer((incoming, response) => {
  if (incoming.method !== 'GET' || incoming.url !== '/') {
    response.writeHead(404).end();
    return;
  }
  response.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' }).end(`<!doctype html>
<meta charset="utf-8">
<title>Cloudlet Browser Acceptance</title>
<h1>Cloudlet Browser Acceptance</h1>
<button id="accept">Click to verify</button>
<script>
const button = document.getElementById('accept');
button.onclick = () => {
  button.textContent = 'Click verified';
  console.info(${JSON.stringify(marker)});
};
</script>
<div style="height: 1000px"></div><p>Full-page footer</p>`);
});

async function run() {
  fixture.listen(0, bridgeAddress);
  await once(fixture, 'listening');
  const url = `http://${bridgeAddress}:${fixture.address().port}/`;
  const login = await request('POST', '/api/v1/auth/login', 200, { email, password });
  cookie = login.headers.getSetCookie().find(value => /^cloudlet_[^=]+_session=/.test(value))?.split(';', 1)[0];
  assert(cookie, '登录响应缺少 Cloudlet 会话 Cookie');
  const session = await (await request('POST', sessions, 201)).json();
  assert(uuid.test(session.id), '创建响应缺少有效的会话标识');
  const endpoint = `${sessions}/${session.id}`;
  let failure;
  try {
    const page = await (await request('POST', endpoint + '/navigate', 200, { url })).json();
    assert.equal(page.title, 'Cloudlet Browser Acceptance');
    await request('POST', endpoint + '/click', 204, { selector: '#accept' });
    const screenshot = await (await request('POST', endpoint + '/screenshots?full_page=true', 201)).json();
    assert(uuid.test(screenshot.id), '截图响应缺少有效截图标识');
    const downloadPath = endpoint + '/screenshots/' + screenshot.id;
    assert.equal(screenshot.download_url, downloadPath);
    const download = await request('GET', downloadPath, 200);
    assert.equal(download.headers.get('content-type'), 'image/png');
    const png = Buffer.from(await download.arrayBuffer());
    const digest = createHash('sha256').update(png).digest('hex');
    assert.equal(digest, screenshot.artifact_digest);
    assert.equal(png.length, screenshot.size_bytes);
    const consoleLog = await (await request('GET', endpoint + '/console', 200)).json();
    assert(consoleLog.entries.some(entry => entry.kind === 'console' && entry.message === marker), '缺少点击后的控制台记录');

    const directory = await mkdtemp(join(tmpdir(), 'cloudlet-browser-acceptance-'));
    await writeFile(join(directory, 'screenshot.png'), png);
    await writeFile(join(directory, 'console.json'), JSON.stringify(consoleLog, null, 2) + '\n');
    return { directory, session_id: session.id, screenshot_id: screenshot.id, size_bytes: png.length, sha256: digest };
  } catch (error) {
    failure = error;
    throw error;
  } finally {
    try {
      const closed = await (await request('DELETE', endpoint, 200)).json();
      assert.equal(closed.status, 'closed');
    } catch (error) {
      throw failure ? new AggregateError([failure, error], '验收和会话清理均失败') : error;
    }
  }
}

try {
  const result = await run();
  console.log(JSON.stringify(result, null, 2));
} finally {
  fixture.close();
}
