import { spawn } from 'node:child_process';
import { createWriteStream } from 'node:fs';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import net from 'node:net';
import path from 'node:path';
import { initialize, loadConfig, root } from '../../scripts/development/config.mjs';
import { connectDocker, run } from '../../scripts/development/process.mjs';
import { acquire, stopController } from '../../scripts/development/controller.mjs';
import { removeResources } from '../../scripts/development/resources.mjs';

export const cleanEnvironment = () => Object.fromEntries(Object.entries(process.env).filter(([name]) => !name.startsWith('CLOUDLET_') && !['ENV_FILE', 'DEBUG', 'CONFIRM'].includes(name)));

export async function environment(name) {
  const directory = path.join(root, '.local', 'checks', name);
  await mkdir(directory, { recursive: true });
  const filename = path.join(directory, '.env.local');
  await initialize(filename);
  const listeners = Array.from({ length: 5 }, () => net.createServer());
  try {
    for (const listener of listeners) await new Promise(resolve => listener.listen(0, '127.0.0.1', resolve));
    const ports = listeners.map(listener => listener.address().port);
    let source = await readFile(filename, 'utf8');
    for (const [before, after] of [['INSTANCE=default', `INSTANCE=${name}`], ['PORT=8080', `PORT=${ports[0]}`], ['PORT=8081', `PORT=${ports[1]}`], ['PORT=5173', `PORT=${ports[2]}`], ['PORT=2345', `PORT=${ports[3]}`], [':15432/', `:${ports[4]}/`]]) source = source.replace(before, after);
    await writeFile(filename, source, { mode: 0o600 });
  } finally {
    for (const listener of listeners) await new Promise(resolve => listener.close(resolve));
  }
  const config = await loadConfig(filename);
  await connectDocker(config);
  return config;
}

export async function start(config, extra = {}) {
  await mkdir(config.directory, { recursive: true });
  const output = createWriteStream(path.join(config.directory, 'check.log'), { flags: 'a' });
  output.on('error', error => { throw error; });
  const child = spawn(process.execPath, ['scripts/dev.mjs', 'start'], {
    cwd: root, env: { ...cleanEnvironment(), ENV_FILE: config.filename, ...extra }, stdio: ['ignore', 'pipe', 'pipe'],
  });
  child.stdout.pipe(output, { end: false });
  child.stderr.pipe(output, { end: false });
  const result = new Promise((resolve, reject) => {
    child.once('error', reject);
    child.once('close', (code, signal) => { output.end(); resolve({ code, signal }); });
  });
  const abort = new AbortController();
  try {
    await Promise.race([
      run(process.execPath, ['scripts/development/wait.mjs', `http-get://${config.api.slice(7)}/readyz`, `http-get://${config.web.slice(7)}`], cleanEnvironment(), { signal: abort.signal }),
      result.then(async value => { throw new Error(`开发入口提前退出 ${JSON.stringify(value)}\n${await readFile(path.join(config.directory, 'check.log'), 'utf8')}`); }),
    ]);
  } catch (error) {
    child.kill('SIGINT');
    await result;
    throw error;
  } finally { abort.abort(); }
  return { child, result };
}

export async function dispose(config) {
  await stopController(config);
  const release = await acquire(config);
  try { await removeResources(config); }
  finally { await release(); }
}
