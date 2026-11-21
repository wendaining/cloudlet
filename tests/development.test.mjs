import assert from 'node:assert/strict';
import { test } from 'node:test';
import { readFile, writeFile, rm } from 'node:fs/promises';
import { spawn } from 'node:child_process';
import path from 'node:path';
import { expect, request } from '@playwright/test';
import { root } from '../scripts/development/config.mjs';
import { assertPortsFree, capture, compose, run } from '../scripts/development/process.mjs';
import { stopController } from '../scripts/development/controller.mjs';
import { environment, start, dispose, cleanEnvironment } from './development/environment.mjs';

const processResult = (config, action, extra = {}) => new Promise((resolve, reject) => {
  const child = spawn(process.execPath, ['scripts/dev.mjs', action], { cwd: root, env: { ...cleanEnvironment(), ENV_FILE: config.filename, ...extra }, stdio: 'ignore' });
  child.once('error', reject);
  child.once('close', code => resolve(code));
});

async function backendPID(config) {
  const source = await readFile(path.join(config.directory, 'development.log'), 'utf8');
  const events = source.split('\n').slice(0, -1).filter(line => line.startsWith('[api] {')).map(line => JSON.parse(line.slice(6)));
  return events.findLast(event => event.msg === 'cloudlet started')?.pid;
}

test('真实本机环境支持热重载、完整停止、多实例和失败清理', { timeout: 300_000 }, async t => {
  const first = await environment(`check-${process.pid}`);
  const second = await environment(`other-${process.pid}`);
  t.after(() => dispose(first));
  t.after(() => dispose(second));
  const probe = path.join(root, 'internal', 'version', `development_check_${process.pid}.go`);
  t.after(() => rm(probe, { force: true }));
  const api = await request.newContext({ baseURL: first.web });
  t.after(() => api.dispose());
  let runtimeVolumes = [];
  t.after(async () => {
    for (const name of runtimeVolumes) assert.equal(await capture('docker', ['volume', 'ls', '-q', '--filter', `name=${name}`], first.env), '');
  });
  let controller = await start(first);
  assert.equal(await processResult(first, 'start'), 1);
  assert.equal(await processResult(first, 'reset', { CONFIRM: 'incorrect-instance' }), 1);
  const login = await api.post('/api/v1/auth/login', { data: { email: first.env.CLOUDLET_DEV_EMAIL, password: first.env.CLOUDLET_DEV_PASSWORD } });
  assert.equal(login.status(), 200);
  const user = await login.json();
  const forwarded = await api.get(first.gateway + '/api/v1/version', { headers: { Host: 'demo.localhost' } });
  assert.equal(forwarded.status(), 200);

  const runtime = await capture('docker', ['run', '-d', '--env', 'POSTGRES_PASSWORD', '--label', 'io.cloudlet.managed=true', '--label', `io.cloudlet.instance=${first.instance}`, '--label', 'io.cloudlet.component=runtime', first.env.CLOUDLET_POSTGRES_IMAGE], { ...first.env, POSTGRES_PASSWORD: first.env.CLOUDLET_POSTGRES_PASSWORD });
  runtimeVolumes = JSON.parse(await capture('docker', ['inspect', '--format', '{{json .Mounts}}', runtime], first.env)).filter(mount => mount.Type === 'volume').map(mount => mount.Name);
  const before = JSON.parse(await capture('docker', ['inspect', '--format', '{{json .State}}', runtime], first.env));
  const oldPID = await backendPID(first);
  assert.equal(typeof oldPID, 'number');
  await writeFile(probe, 'package version\n\nconst developmentCheck = true\n');
  await expect.poll(() => backendPID(first), { timeout: 60_000 }).not.toBe(oldPID);
  assert.equal((await api.get('/api/v1/auth/session')).status(), 200);
  const after = JSON.parse(await capture('docker', ['inspect', '--format', '{{json .State}}', runtime], first.env));
  assert.equal(after.Running, true);
  assert.equal(after.StartedAt, before.StartedAt);

  await writeFile(probe, 'package version\n\nfunc incomplete(\n');
  await expect.poll(async () => {
    try { await assertPortsFree([Number(first.env.CLOUDLET_API_PORT)]); return true; }
    catch (error) { if (error.message.includes('EADDRINUSE')) return false; throw error; }
  }, { timeout: 60_000 }).toBe(true);
  assert.equal(await capture('docker', ['inspect', '--format', '{{.State.Running}}', runtime], first.env), 'true');
  await writeFile(probe, 'package version\n\nconst developmentCheck = false\n');
  await run(process.execPath, ['scripts/development/wait.mjs', `http-get://${first.api.slice(7)}/readyz`], cleanEnvironment());
  assert.equal((await api.get('/api/v1/auth/session')).status(), 200);

  const other = await start(second);
  assert.equal((await api.get(second.web + '/api/v1/auth/session')).status(), 401);
  await stopController(first);
  assert.equal((await controller.result).code, 0);
  await assertPortsFree(first.ports);
  assert.equal(await capture('docker', ['inspect', '--format', '{{.State.Running}}', runtime], first.env), 'false');
  assert.equal((await api.get(second.api + '/readyz')).status(), 200);
  controller = await start(first);
  const restored = await api.get('/api/v1/auth/session');
  assert.equal(restored.status(), 200);
  assert.equal((await restored.json()).id, user.id);
  controller.child.kill('SIGINT');
  assert.equal((await controller.result).code, 0);
  await assertPortsFree(first.ports);
  assert.equal((await api.get(second.api + '/readyz')).status(), 200);
  await stopController(second);
  assert.equal((await other.result).code, 0);

  await compose(first, ['up', '-d', '--wait', '--wait-timeout', '90', '--quiet-pull']);
  await compose(first, ['exec', '-T', 'postgres', 'psql', '-U', first.env.CLOUDLET_POSTGRES_USER, '-d', first.env.CLOUDLET_POSTGRES_DB, '-v', 'ON_ERROR_STOP=1', '-c', 'INSERT INTO goose_db_version (version_id, is_applied) VALUES (3, TRUE)']);
  await compose(first, ['stop', '--timeout', '10']);
  assert.equal(await processResult(first, 'start'), 1);
  await assertPortsFree(first.ports);
  const ids = await compose(first, ['ps', '--status', 'running', '-q'], false);
  assert.equal(ids, '');
});

test('数据库初始化期间可以完整退出', { timeout: 90_000 }, async t => {
  const config = await environment(`cancel-${process.pid}`);
  t.after(() => dispose(config));
  const child = spawn(process.execPath, ['scripts/dev.mjs', 'start'], { cwd: root, env: { ...cleanEnvironment(), ENV_FILE: config.filename }, stdio: 'ignore' });
  const result = new Promise((resolve, reject) => { child.once('error', reject); child.once('close', code => resolve(code)); });
  t.after(async () => { if (child.exitCode === null) { child.kill('SIGINT'); await result; } });
  await Promise.race([
    expect.poll(() => capture('docker', ['ps', '-q', '--filter', `label=io.cloudlet.instance=${config.instance}`], config.env), { timeout: 30_000 }).not.toBe(''),
    result.then(code => { throw new Error(`开发环境在数据库启动前退出，退出码 ${code}`); }),
  ]);
  child.kill('SIGINT');
  assert.equal(await result, 0);
  await assertPortsFree(config.ports);
  assert.equal(await compose(config, ['ps', '--status', 'running', '-q'], false), '');
});
