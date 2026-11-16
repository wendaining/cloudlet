import { initialize, loadConfig } from './development/config.mjs';
import { connectDocker, doctor, run } from './development/process.mjs';

const action = process.argv[2];
if (action === 'init') await initialize();
else if (action === 'doctor') await doctor(await loadConfig());
else if (action === 'test-integration') {
  const config = await loadConfig();
  await connectDocker(config);
  await run('go', ['test', '-race', '-count=1', '-tags=integration', './internal/database'], config.env);
}
else throw new Error(`未知开发命令：${action}`);
