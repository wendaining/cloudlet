import { test } from 'node:test';
import { run } from '../scripts/development/process.mjs';
import { environment, start, dispose, cleanEnvironment } from './development/environment.mjs';

test('真实浏览器完成登录、刷新、退出与页面布局检查', { timeout: 240_000 }, async t => {
  const config = await environment(`browser-${process.pid}`);
  t.after(() => dispose(config));
  await start(config);
  await run('npx', ['playwright', 'test'], { ...cleanEnvironment(), ENV_FILE: config.filename, CLOUDLET_TEST_URL: config.web });
});
