import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtemp, readFile, writeFile, rm, realpath, symlink } from 'node:fs/promises';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { initialize, loadConfig } from './config.mjs';
import { assertPortsFree } from './process.mjs';

test('初始化保留已有凭据并拒绝错误配置', async t => {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'cloudlet-config-'));
  t.after(() => rm(directory, { recursive: true }));
  const filename = path.join(directory, '.env.local');
  await initialize(filename);
  const first = await readFile(filename, 'utf8');
  await initialize(filename);
  assert.equal(await readFile(filename, 'utf8'), first);
  const config = await loadConfig(filename);
  const alias = path.join(directory, 'linked.env');
  await symlink(filename, alias);
  assert.equal((await loadConfig(alias)).filename, await realpath(filename));
  assert.equal(config.databasePort, 15432);
  assert.equal(config.env.COMPOSE_PROJECT_NAME, 'cloudlet-default');
  await writeFile(filename, first.replace('CLOUDLET_WEB_PORT=5173', 'CLOUDLET_WEB_PORT=8080'));
  await assert.rejects(loadConfig(filename), /监听端口不能重复/u);
  await writeFile(filename, first + '\nCLOUDLET_TYPO=true\n');
  await assert.rejects(loadConfig(filename), /CLOUDLET_TYPO/u);
});

test('占用真实端口时返回错误', async t => {
  const listener = net.createServer();
  await new Promise(resolve => listener.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => listener.close(resolve)));
  await assert.rejects(assertPortsFree([listener.address().port]), /不可用/u);
});

test('已有配置缺少平台密钥时补充密钥', async t => {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'cloudlet-config-'));
  t.after(() => rm(directory, { recursive: true }));
  const filename = path.join(directory, '.env.local');
  await writeFile(filename, [
    'CLOUDLET_ENV=development',
    'CLOUDLET_INSTANCE=default',
    'CLOUDLET_API_PORT=8080',
    'CLOUDLET_GATEWAY_PORT=8081',
    'CLOUDLET_WEB_PORT=5173',
    'CLOUDLET_DEBUG_PORT=2345',
    'CLOUDLET_DATABASE_URL=postgres://cloudlet:database-password@127.0.0.1:15432/cloudlet?sslmode=disable',
    'CLOUDLET_DEV_EMAIL=developer@cloudlet.local',
    'CLOUDLET_DEV_PASSWORD=account-password-1234',
    '',
  ].join('\n'));
  await initialize(filename);
  const content = await readFile(filename, 'utf8');
  assert.match(content, /^CLOUDLET_SECRET_KEY=[0-9a-f]{64}$/mu);
  await loadConfig(filename);
});
