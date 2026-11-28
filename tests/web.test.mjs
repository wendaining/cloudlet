import { test } from 'node:test';
import { capture, run } from '../scripts/development/process.mjs';
import { environment, start, dispose, cleanEnvironment } from './development/environment.mjs';
import { root } from '../scripts/development/config.mjs';
import path from 'node:path';

test('真实浏览器验证控制台与 Browser Run 导航', { timeout: 420_000 }, async t => {
  const config = await environment(`browser-${process.pid}`);
  t.after(() => dispose(config));

  const image = await capture('docker', ['build', '--quiet', 'runtimes/browser'], config.env);
  // Browser Run containers use the default bridge. Host the test page on the same
  // bridge so Chromium can reach it on both Linux Docker and Docker Desktop.
  const fixture = await capture('docker', [
    'run', '--detach', '--rm', '--network', 'bridge', '--entrypoint', '/usr/local/bin/node', image,
    '-e', `require('node:http').createServer((request, response) => {
      if (request.url === '/slow') return;
      if (request.url === '/redirect') {
        response.writeHead(302, { Location: '/page' }).end();
        return;
      }
      response.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
      response.end('<!doctype html><title>Cloudlet 浏览器验收</title>');
    }).listen(8080, '0.0.0.0');`,
  ], config.env);
  t.after(() => run('docker', ['rm', '--force', fixture], config.env));
  const fixtureIP = await capture('docker', [
    'inspect', '--format', '{{.NetworkSettings.Networks.bridge.IPAddress}}', fixture,
  ], config.env);
  if (!fixtureIP) throw new Error('测试页面容器缺少 Docker bridge 地址');

  await start(config, {
    CLOUDLET_BROWSER_IMAGE: image,
    CLOUDLET_BROWSER_SECCOMP: path.join(root, 'runtimes/browser/seccomp.json'),
  });
  await run('npx', ['playwright', 'test'], {
    ...cleanEnvironment(),
    ENV_FILE: config.filename,
    CLOUDLET_TEST_URL: config.web,
    CLOUDLET_TEST_BROWSER_URL: 'http://' + fixtureIP + ':8080',
  });
});
