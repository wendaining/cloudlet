import { initialize, loadConfig } from './development/config.mjs';
import { doctor } from './development/process.mjs';

const action = process.argv[2];
if (action === 'init') await initialize();
else if (action === 'doctor') await doctor(await loadConfig());
else throw new Error(`未知开发命令：${action}`);
