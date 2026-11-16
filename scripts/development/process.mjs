import { execFile, spawn } from 'node:child_process';
import { promisify } from 'node:util';
import net from 'node:net';
import treeKill from 'tree-kill';
import { root } from './config.mjs';

const execute = promisify(execFile);

export async function capture(command, args, env = process.env) {
  const { stdout } = await execute(command, args, { cwd: root, env, timeout: 120_000, maxBuffer: 4 * 1024 * 1024 });
  return stdout.trim();
}

export function run(command, args, env = process.env, { signal } = {}) {
  signal?.throwIfAborted();
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { cwd: root, env, stdio: 'inherit' });
    let force;
    const interrupt = () => {
      if (child.exitCode !== null || child.signalCode !== null) return;
      treeKill(child.pid, 'SIGINT', error => { if (error && child.exitCode === null && child.signalCode === null) reject(error); });
      force = setTimeout(() => treeKill(child.pid, 'SIGKILL'), 10_000);
      force.unref();
    };
    signal?.addEventListener('abort', interrupt, { once: true });
    child.once('error', reject);
    child.once('close', (code, exitSignal) => {
      clearTimeout(force);
      signal?.removeEventListener('abort', interrupt);
      if (signal?.aborted) reject(signal.reason);
      else if (code === 0) resolve();
      else reject(new Error(`${command} 执行失败，退出码 ${code}，信号 ${exitSignal}`));
    });
  });
}

export async function connectDocker(config) {
  const env = { ...config.env };
  const context = env.DOCKER_CONTEXT || await capture('docker', ['context', 'show'], env);
  const host = env.DOCKER_HOST || await capture('docker', ['context', 'inspect', context, '--format', '{{.Endpoints.docker.Host}}'], env);
  if (!host.startsWith('unix://')) throw new Error('本机开发需要使用 unix:// Docker 连接');
  delete env.DOCKER_CONTEXT;
  env.DOCKER_HOST = host;
  env.TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE = '/var/run/docker.sock';
  const server = JSON.parse(await capture('docker', ['version', '--format', '{{json .Server}}'], env));
  if (server.Os !== 'linux' || !['amd64', 'arm64'].includes(server.Arch)) {
    throw new Error('Docker 需要提供 amd64 或 arm64 的 Linux 容器环境');
  }
  config.env = env;
  return { context, version: server.Version, architecture: server.Arch };
}

export function compose(config, args, live = true) {
  const parameters = ['compose', '--project-directory', root, '--env-file', config.filename,
    '-f', 'compose.yaml', '-f', 'compose.dev.yaml', ...args];
  return live ? run('docker', parameters, config.env, { signal: config.signal }) : capture('docker', parameters, config.env);
}

export async function assertPortsFree(ports) {
  for (const port of ports) {
    await new Promise((resolve, reject) => {
      const listener = net.createServer();
      listener.once('error', error => reject(new Error(`端口 ${port} 不可用：${error.code}`)));
      listener.listen(port, '127.0.0.1', () => listener.close(resolve));
    });
  }
}

export async function doctor(config, { ports = true } = {}) {
  if (Number(process.versions.node.split('.')[0]) < 24) throw new Error('需要 Node.js 24 或更新版本');
  const go = await capture('go', ['version'], config.env);
  await capture('go', ['list', '-m', '-json'], config.env);
  await capture('shellcheck', ['--version'], config.env);
  const composeVersion = await capture('docker', ['compose', 'version'], config.env);
  const docker = await connectDocker(config);
  await compose(config, ['config', '--quiet'], false);
  if (ports) await assertPortsFree(config.ports);
  console.log(`实例：${config.instance}，Node.js ${process.versions.node}，${go}`);
  console.log(`${composeVersion}，Docker ${docker.version}，${docker.architecture}，context ${docker.context}`);
  console.log(`页面：${config.web}，API：${config.api}，Gateway：${config.gateway}`);
}
