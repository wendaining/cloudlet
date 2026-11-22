import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { createServer } from 'node:http';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { setTimeout as delay } from 'node:timers/promises';
import { promisify } from 'node:util';

const execute = promisify(execFile);
const directory = fileURLToPath(new URL('.', import.meta.url));
const seccomp = fileURLToPath(new URL('./seccomp.json', import.meta.url));
const docker = (args, env = process.env) => execute('docker', args, { cwd: directory, env, timeout: 240000 });

test('non-root Chromium container navigates and shuts down', { timeout: 300000 }, async (t) => {
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

  const fixture = createServer((request, response) => {
    if (request.url === '/slow') return;
    response.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
    response.end('<!doctype html><title>Cloudlet Browser Run</title>');
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

  const { stdout: endpoint } = await docker(['port', name, '8080/tcp']);
  const origin = `http://${endpoint.trim()}`;
  let ready;
  for (let i = 0; i < 100; i++) {
    ready = await fetch(`${origin}/readyz`).catch(() => undefined);
    if (ready?.ok) break;
    await ready?.body?.cancel();
    const { stdout: running } = await docker(['inspect', '--format', '{{.State.Running}}', name]);
    if (running.trim() !== 'true') break;
    await delay(150);
  }
  if (!ready?.ok) {
    const { stdout, stderr } = await docker(['logs', name]);
    assert.fail(`Browser Run did not become ready:\n${stdout}${stderr}`);
  }
  assert.deepEqual(await ready.json(), { status: 'ready' });
  const { stdout: uid } = await docker(['exec', name, 'id', '-u']);
  assert.notEqual(uid.trim(), '0');
  const { stdout: nodeVersion } = await docker(['exec', name, '/usr/local/bin/node', '--version']);
  assert.equal(nodeVersion.trim(), 'v24.22.2');

  const url = `http://host.test:${fixture.address().port}`;
  const navigate = (input, key = token) => fetch(`${origin}/navigate`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${key}` },
    body: JSON.stringify(input),
  });
  let response = await navigate({ url }, randomBytes(32).toString('hex'));
  assert.equal(response.status, 401);
  assert.deepEqual(await response.json(), { error: 'unauthorized' });

  response = await navigate({ url: 'file:///tmp/test.html' });
  assert.equal(response.status, 400);
  assert.deepEqual(await response.json(), { error: 'invalid_url' });

  response = await navigate({ url: `${url}/slow`, timeout_ms: 200 });
  assert.equal(response.status, 504);
  assert.deepEqual(await response.json(), { error: 'navigation_timeout' });

  const page = `${url}/page?example=1`;
  response = await navigate({ url: page });
  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), { url: page, title: 'Cloudlet Browser Run' });

  const { stdout: processes } = await docker(['top', name, '-eo', 'pid,args']);
  assert.match(processes, /chrome/);
  assert.doesNotMatch(processes, /(?:^|\s)--no-sandbox(?:\s|$)/m);
  await docker(['stop', '--timeout', '8', name]);
  const { stdout: state } = await docker(['inspect', '--format', '{{.State.Running}} {{.State.ExitCode}}', name]);
  assert.equal(state.trim(), 'false 0');
});
