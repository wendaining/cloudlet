import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { createServer } from 'node:http';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { setTimeout as delay } from 'node:timers/promises';
import { promisify } from 'node:util';
import { PNG } from 'pngjs';
import { readSessionDeadline } from './session-deadline.ts';

const execute = promisify(execFile);
const directory = fileURLToPath(new URL('.', import.meta.url));
const seccomp = fileURLToPath(new URL('./seccomp.json', import.meta.url));
const docker = (args, env = process.env) => execute('docker', args, { cwd: directory, env, timeout: 240000 });

test('validates UTC session deadline and rejects expired or invalid configuration', () => {
  const now = Date.parse('2030-01-01T00:00:00Z');
  assert.equal(readSessionDeadline(undefined, now), undefined);
  assert.equal(readSessionDeadline('2030-01-01T00:00:00.001Z', now), now + 1);
  assert.equal(readSessionDeadline('2030-01-01T00:00:01Z', now), now + 1000);
  assert.throws(() => readSessionDeadline('2030-01-01T00:00:00Z', now), /future/);
  assert.throws(() => readSessionDeadline('2029-12-31T23:59:59Z', now), /future/);
  for (const value of ['', '2030-02-30T00:00:00Z', '2030-01-01T00:00:00+00:00']) {
    assert.throws(() => readSessionDeadline(value, now), /UTC timestamp/);
  }
});

test('non-root Chromium navigates, captures PNG and shuts down', { timeout: 300000 }, async (t) => {
  const token = randomBytes(32).toString('hex');
  const name = `cloudlet-browser-test-${randomBytes(6).toString('hex')}`;
  const image = `${name}:test`;
  await docker(['build', '-t', image, '.']);
  let created = false;
  t.after(async () => {
    try {
      if (created) await docker(['rm', '-f', name]);
    } finally {
      await docker(['image', 'rm', image]);
    }
  });

  const pendingFont = Promise.withResolvers();
  const fixture = createServer((request, response) => {
    if (request.url === '/slow') return;
    if (request.url === '/slow.woff2') {
      pendingFont.resolve();
      return;
    }
    response.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
    const html = '<!doctype html><title>Cloudlet Browser Run</title>';
    if (request.url === '/pending-font') {
      response.end(`${html}<style>@font-face { font-family: Slow; src: url('/slow.woff2'); }
        body { font-family: Slow, sans-serif; }</style><body>Pending font</body>`);
    } else if (request.url === '/large-image') {
      response.end(`${html}<style>body { margin: 0; }</style><canvas width="1280" height="720"></canvas>
        <script>
          const context = document.querySelector('canvas').getContext('2d');
          const image = context.createImageData(1280, 720);
          let seed = 1;
          for (let i = 0; i < image.data.length; i += 4) {
            seed ^= seed << 13; seed ^= seed >>> 17; seed ^= seed << 5;
            image.data[i] = seed & 255;
            image.data[i + 1] = (seed >>> 8) & 255;
            image.data[i + 2] = (seed >>> 16) & 255;
            image.data[i + 3] = 255;
          }
          context.putImageData(image, 0, 0);
        </script>`);
    } else {
      response.end(`${html}<style>body { background: #173e71; }</style>`);
    }
  });
  await new Promise((resolve, reject) => {
    fixture.once('error', reject);
    fixture.listen(0, '0.0.0.0', resolve);
  });
  t.after(() => {
    fixture.close();
    fixture.closeAllConnections();
  });

  await docker([
    'create', '--name', name, '--init', '--cap-drop=ALL', '--shm-size=256m',
    '--security-opt', 'no-new-privileges', '--security-opt', `seccomp=${seccomp}`,
    '--add-host', 'host.test:host-gateway', '-p', '127.0.0.1::8080',
    '-e', 'CLOUDLET_BROWSER_TOKEN', image,
  ], { ...process.env, CLOUDLET_BROWSER_TOKEN: token });
  created = true;
  await docker(['start', name]);

  async function waitReady() {
    const { stdout: endpoint } = await docker(['port', name, '8080/tcp']);
    const origin = `http://${endpoint.trim()}`;
    for (let i = 0; i < 100; i++) {
      const ready = await fetch(`${origin}/readyz`).catch(() => undefined);
      if (ready?.ok) {
        assert.deepEqual(await ready.json(), { status: 'ready' });
        return origin;
      }
      await ready?.body?.cancel();
      const { stdout: running } = await docker(['inspect', '--format', '{{.State.Running}}', name]);
      if (running.trim() !== 'true') break;
      await delay(150);
    }
    const { stdout, stderr } = await docker(['logs', name]);
    assert.fail(`Browser Run did not become ready:\n${stdout}${stderr}`);
  }
  async function assertStopped() {
    for (let i = 0; i < 50; i++) {
      const { stdout: state } = await docker(['inspect', '--format', '{{.State.Running}} {{.State.ExitCode}}', name]);
      if (state.startsWith('false ')) {
        assert.equal(state.trim(), 'false 0');
        return;
      }
      await delay(100);
    }
    assert.fail('Browser Run did not exit');
  }
  let origin = await waitReady();
  const { stdout: uid } = await docker(['exec', name, 'id', '-u']);
  assert.notEqual(uid.trim(), '0');
  const { stdout: nodeVersion } = await docker(['exec', name, '/usr/local/bin/node', '--version']);
  assert.equal(nodeVersion.trim(), 'v24.22.2');

  const expiring = `${name}-expiry`;
  const deadline = Date.now() + 6000;
  await docker([
    'create', '--name', expiring, '--init', '--cap-drop=ALL', '--shm-size=256m',
    '--security-opt', 'no-new-privileges', '--security-opt', `seccomp=${seccomp}`,
    '-p', '127.0.0.1::8080', '-e', 'CLOUDLET_BROWSER_TOKEN',
    '-e', `CLOUDLET_BROWSER_DEADLINE=${new Date(deadline).toISOString()}`, image,
  ], { ...process.env, CLOUDLET_BROWSER_TOKEN: token });
  try {
    await docker(['start', expiring]);
    const { stdout: endpoint } = await docker(['port', expiring, '8080/tcp']);
    const expiringOrigin = `http://${endpoint.trim()}`;
    let ready = false;
    while (Date.now() < deadline) {
      const response = await fetch(`${expiringOrigin}/readyz`, { signal: AbortSignal.timeout(500) }).catch(() => undefined);
      if (response?.ok) {
        assert.deepEqual(await response.json(), { status: 'ready' });
        ready = true;
        break;
      }
      await delay(100);
    }
    assert.equal(ready, true, 'Chromium must start before expiry');
    const { stdout: processors } = await docker(['top', expiring, '-eo', 'pid,args']);
    assert.match(processors, /chrome/);
    const { stdout: code } = await execute('docker', ['wait', expiring], { cwd: directory, timeout: 12000 });
    assert.equal(code.trim(), '0');
    assert.ok(Date.now() >= deadline);
    assert.equal((await docker(['inspect', '--format', '{{.State.Running}}', expiring])).stdout.trim(), 'false');
  } finally {
    await docker(['rm', '-f', expiring]);
  }

  const url = `http://host.test:${fixture.address().port}`;
  const command = (path, input, key = token) => fetch(`${origin}${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${key}` },
    body: JSON.stringify(input),
    signal: AbortSignal.timeout(12000),
  });
  const navigate = (input, key) => command('/navigate', input, key);
  const screenshot = (input, key) => command('/screenshot', input, key);
  const invalidToken = randomBytes(32).toString('hex');
  let response = await navigate({ url }, invalidToken);
  assert.equal(response.status, 401);
  assert.deepEqual(await response.json(), { error: 'unauthorized' });

  response = await navigate({ url: 'file:///tmp/test.html' });
  assert.equal(response.status, 400);
  assert.deepEqual(await response.json(), { error: 'invalid_url' });

  response = await screenshot(undefined, invalidToken);
  assert.equal(response.status, 401);
  assert.deepEqual(await response.json(), { error: 'unauthorized' });
  response = await screenshot();
  assert.equal(response.status, 409);
  assert.deepEqual(await response.json(), { error: 'no_page' });
  response = await screenshot({ timeout_ms: 0 });
  assert.equal(response.status, 400);
  assert.deepEqual(await response.json(), { error: 'invalid_timeout' });

  const page = `${url}/page?example=1`;
  response = await navigate({ url: page });
  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), { url: page, title: 'Cloudlet Browser Run' });

  response = await screenshot();
  assert.equal(response.status, 200);
  assert.equal(response.headers.get('Content-Type'), 'image/png');
  const png = Buffer.from(await response.arrayBuffer());
  assert.equal(Number(response.headers.get('Content-Length')), png.length);
  const decoded = PNG.sync.read(png);
  assert.deepEqual([decoded.width, decoded.height], [1280, 720]);
  assert.deepEqual([...decoded.data.subarray(0, 4)], [0x17, 0x3e, 0x71, 255]);

  response = await navigate({ url: `${url}/large-image` });
  assert.equal(response.status, 200);
  await response.json();
  response = await screenshot();
  assert.equal(response.status, 422);
  assert.deepEqual(await response.json(), { error: 'screenshot_too_large' });

  const { stdout: processes } = await docker(['top', name, '-eo', 'pid,args']);
  assert.match(processes, /chrome/);
  assert.doesNotMatch(processes, /(?:^|\s)--no-sandbox(?:\s|$)/m);

  response = await navigate({ url: `${url}/pending-font` });
  assert.equal(response.status, 200);
  await response.json();
  await Promise.race([pendingFont.promise, delay(3000, undefined, { ref: false }).then(() => assert.fail('Chromium did not request the test font'))]);
  response = await screenshot({ timeout_ms: 300 });
  assert.equal(response.status, 504);
  assert.deepEqual(await response.json(), { error: 'screenshot_timeout' });
  await assertStopped();

  await docker(['start', name]);
  origin = await waitReady();

  response = await navigate({ url: `${url}/slow`, timeout_ms: 200 });
  assert.equal(response.status, 504);
  assert.deepEqual(await response.json(), { error: 'navigation_timeout' });
  await assertStopped();

  await docker(['start', name]);
  await waitReady();
  await docker(['stop', '--timeout', '8', name]);
  await assertStopped();
});
