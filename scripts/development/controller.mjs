import { createWriteStream } from 'node:fs';
import { chmod, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import net from 'node:net';
import path from 'node:path';
import { PassThrough } from 'node:stream';
import { setTimeout as delay } from 'node:timers/promises';
import { createInterface } from 'node:readline';
import lockfile from 'proper-lockfile';
import concurrently from 'concurrently';
import { parse, stringify } from 'smol-toml';
import { quote } from 'shell-quote';
import { root, writeServerConfig } from './config.mjs';
import { capture, compose, doctor, run } from './process.mjs';
import { stopResources } from './resources.mjs';

const lockOptions = { realpath: false, stale: 10_000, update: 2000 };
export const locked = config => lockfile.check(config.controlPath, lockOptions);

export async function acquire(config) {
  try { return await lockfile.lock(config.controlPath, lockOptions); }
  catch (error) {
    if (error.code !== 'ELOCKED') throw error;
    throw new Error(`实例 ${config.instance} 已有开发控制器，请使用 dev-status 查看`);
  }
}

async function airConfig(config) {
  const source = parse(await readFile(path.join(root, '.air.toml'), 'utf8'));
  const binary = path.join(config.directory, 'server');
  const commit = await capture('git', ['rev-parse', 'HEAD'], config.env);
  source.tmp_dir = path.join(config.directory, 'air');
  source.build.cmd = quote(['go', 'build', ...(process.env.DEBUG === 'api' ? ['-gcflags=all=-N -l'] : []), '-ldflags', `-X github.com/wendaining/cloudlet/internal/version.Version=dev -X github.com/wendaining/cloudlet/internal/version.Commit=${commit}`, '-o', binary, './cmd/server']);
  source.build.entrypoint = process.env.DEBUG === 'api'
    ? ['go', 'tool', 'dlv', 'exec', binary, '--headless', `--listen=127.0.0.1:${config.env.CLOUDLET_DEBUG_PORT}`, '--api-version=2', '--accept-multiclient', '--continue', '--', '-config', config.env.CLOUDLET_CONFIG]
    : [binary, '-config', config.env.CLOUDLET_CONFIG];
  const filename = path.join(config.directory, 'air.toml');
  await writeFile(filename, stringify(source), { mode: 0o600 });
  return filename;
}

export function frontendEnvironment(config) {
  const inherited = Object.fromEntries(Object.entries(process.env).filter(([name]) => !name.startsWith('CLOUDLET_') && !name.startsWith('DOCKER_')));
  return { ...inherited, CLOUDLET_WEB_PORT: config.env.CLOUDLET_WEB_PORT, CLOUDLET_API_UPSTREAM: config.api };
}

export async function admin(config, action) {
  await run('go', ['run', './cmd/admin', action], config.env, { signal: config.signal });
}

export async function develop(config) {
  if (process.env.DEBUG && process.env.DEBUG !== 'api') throw new Error('DEBUG 仅支持 api');
  const release = await acquire(config);
  const socketPath = config.controlPath + '.sock';
  const abort = new AbortController();
  const readinessAbort = new AbortController();
  let group;
  let groupResult;
  let started = false;
  let failure;
  let force;
  const stopGroup = () => {
    for (const command of group?.commands ?? []) command.kill('SIGINT');
    if (group && !force) {
      force = setTimeout(() => { for (const command of group.commands) command.kill('SIGKILL'); }, 12_000);
      force.unref();
    }
  };
  const requestStop = () => {
    if (abort.signal.aborted) return;
    abort.abort();
    readinessAbort.abort();
    stopGroup();
  };
  const control = net.createServer(socket => {
    socket.setTimeout(2000, () => socket.destroy());
    const input = createInterface({ input: socket });
    input.once('line', line => {
      if (line === `stop ${config.filename}`) { socket.end('ok\n'); requestStop(); }
      else socket.end('unknown\n');
    });
    socket.on('error', error => { if (error.code !== 'ECONNRESET') { failure = error; requestStop(); } });
  });
  process.on('SIGINT', requestStop);
  process.on('SIGTERM', requestStop);
  try {
    await mkdir(config.directory, { recursive: true, mode: 0o700 });
    await rm(socketPath, { force: true });
    await new Promise((resolve, reject) => { control.once('error', reject); control.listen(socketPath, resolve); });
    await chmod(socketPath, 0o600);
    await writeFile(path.join(config.directory, 'controller.json'), JSON.stringify({ pid: process.pid, root, config: config.filename, started: new Date().toISOString() }) + '\n', { mode: 0o600 });
    await doctor(config);
    started = true;
    config.signal = abort.signal;
    await compose(config, ['up', '-d', '--wait', '--wait-timeout', '90', '--quiet-pull']);
    abort.signal.throwIfAborted();
    await admin(config, 'migrate');
    abort.signal.throwIfAborted();
    await admin(config, 'seed-development');
    await writeServerConfig(config);
    const air = await airConfig(config);
    abort.signal.throwIfAborted();
    const log = createWriteStream(path.join(config.directory, 'development.log'), { flags: 'a', mode: 0o600 });
    const output = new PassThrough();
    output.pipe(process.stdout, { end: false });
    output.pipe(log);
    group = concurrently([
      { name: 'api', command: quote(['go', 'tool', 'air', '-c', air]), env: config.env },
      { name: 'web', command: 'npm run dev --workspace web', env: frontendEnvironment(config) },
      { name: 'types', command: 'npm run typecheck:watch --workspace web', env: frontendEnvironment(config) },
    ], { cwd: root, outputStream: output, prefix: '[{name}]', prefixColors: ['reset'], killOthersOn: ['success', 'failure'], killSignal: 'SIGINT', killTimeout: 12_000, successCondition: 'all' });
    groupResult = group.result.then(() => null, events => new Error(`开发进程退出：${events.map(event => `${event.command.name}=${event.exitCode}`).join('，')}`));
    try {
      const readiness = run(process.execPath, [path.join(root, 'scripts/development/wait.mjs'), `http-get://${config.api.slice(7)}/readyz`, `http-get://${config.web.slice(7)}`], frontendEnvironment(config), { signal: readinessAbort.signal });
      const result = await Promise.race([readiness, groupResult]);
      if (result instanceof Error) throw result;
      abort.signal.throwIfAborted();
      console.log(`开发环境已就绪：${config.web}`);
      console.log(`开发账号与密码保存在 ${config.filename}`);
      const exit = await groupResult;
      if (!abort.signal.aborted) throw exit ?? new Error('开发进程已退出');
    } finally {
      readinessAbort.abort();
      stopGroup();
      await groupResult;
      clearTimeout(force);
      await new Promise(resolve => { log.once('finish', resolve); output.end(); });
    }
  } catch (error) {
    if (!abort.signal.aborted) failure = error;
  } finally {
    process.off('SIGINT', requestStop);
    process.off('SIGTERM', requestStop);
    delete config.signal;
    try { if (started) await stopResources(config); }
    catch (error) { failure = failure ? new AggregateError([failure, error], '启动与资源停止失败') : error; }
    await new Promise(resolve => control.close(resolve));
    await rm(socketPath, { force: true });
    await rm(path.join(config.directory, 'controller.json'), { force: true });
    await release();
  }
  if (failure) throw failure;
  console.log(`实例 ${config.instance} 已停止，持久化数据已保留`);
}

export async function stopController(config) {
  if (!await locked(config)) return false;
  await new Promise((resolve, reject) => {
    const socket = net.createConnection(config.controlPath + '.sock');
    socket.setTimeout(5000, () => socket.destroy(new Error('开发控制器响应超时')));
    socket.once('error', reject);
    socket.once('connect', () => socket.write(`stop ${config.filename}\n`));
    const input = createInterface({ input: socket });
    input.once('line', line => { socket.end(); if (line === 'ok') resolve(); else reject(new Error('开发控制器未接受停止请求')); });
  });
  const deadline = Date.now() + 60_000;
  while (await locked(config)) {
    if (Date.now() > deadline) throw new Error('开发环境停止超时，请查看本实例日志和容器状态');
    await delay(200);
  }
  return true;
}
