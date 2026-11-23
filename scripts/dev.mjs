import { readFile, rm } from 'node:fs/promises';
import path from 'node:path';
import { initialize, loadConfig } from './development/config.mjs';
import { compose, connectDocker, doctor, run } from './development/process.mjs';
import { acquire, admin, develop, locked, stopController } from './development/controller.mjs';
import { removeResources, resourceStatus, stopResources } from './development/resources.mjs';

const action = process.argv[2];
if (action === 'init') await initialize();
else if (action === 'doctor') await doctor(await loadConfig());
else if (action === 'start') await develop(await loadConfig());
else if (action === 'stop' || action === 'reset') {
  const config = await loadConfig();
  if (action === 'reset' && process.env.CONFIRM !== config.instance) {
    throw new Error(`清理范围为实例 ${config.instance} 的数据库、执行容器和持久化数据卷。确认后执行 make dev-reset CONFIRM=${config.instance}`);
  }
  await stopController(config);
  const release = await acquire(config);
  try {
    await connectDocker(config);
    if (action === 'reset') {
      await removeResources(config);
      await rm(path.join(config.directory, 'server.json'), { force: true });
      console.log(`实例 ${config.instance} 的数据与资源已清理`);
    } else {
      await stopResources(config);
      console.log(`实例 ${config.instance} 已停止，持久化数据已保留`);
    }
  } finally { await release(); }
}
else if (action === 'status') {
  const config = await loadConfig();
  const active = await locked(config);
  console.log(`实例：${config.instance}，控制器：${active ? '运行中' : '已停止'}`);
  if (active) console.log(await readFile(path.join(config.directory, 'controller.json'), 'utf8'));
  console.log(`页面：${config.web}，API：${config.api}，Gateway：${config.gateway}`);
  await connectDocker(config);
  await compose(config, ['ps', '--all']);
  console.log(await resourceStatus(config));
  const databases = await compose(config, ['ps', '--status', 'running', '-q', 'postgres'], false);
  if (databases) await admin(config, 'db-status');
  else console.log('数据库已停止');
}
else if (action === 'logs') {
  const config = await loadConfig();
  await run('tail', ['-n', '100', '-F', path.join(config.directory, 'development.log')]);
}
else if (action === 'migrate' || action === 'db-status') {
  await admin(await loadConfig(), action);
}
else if (action === 'test-integration') {
  const config = await loadConfig();
  await connectDocker(config);
  await run('go', ['test', '-race', '-count=1', '-tags=integration', './internal/database', './internal/capacity', './internal/controlplane', './internal/executions', './internal/workers', './internal/containers'], config.env);
}
else throw new Error(`未知开发命令：${action}`);
