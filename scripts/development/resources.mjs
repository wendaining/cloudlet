import { capture, compose } from './process.mjs';

const filters = config => ['--filter', 'label=io.cloudlet.managed=true', '--filter', `label=io.cloudlet.instance=${config.instance}`, '--filter', 'label=io.cloudlet.component=runtime'];
const identifiers = value => value ? value.split('\n') : [];

export async function stopResources(config) {
  const failures = [];
  try {
    const running = identifiers(await capture('docker', ['ps', '-q', ...filters(config)], config.env));
    if (running.length) await capture('docker', ['stop', '--time', '10', ...running], config.env);
    const remaining = identifiers(await capture('docker', ['ps', '-q', ...filters(config)], config.env));
    if (remaining.length) throw new Error(`尚未停止的执行容器：${remaining.join('，')}`);
  } catch (error) { failures.push(error); }
  try { await compose(config, ['stop', '--timeout', '10']); }
  catch (error) { failures.push(error); }
  if (failures.length) throw new AggregateError(failures, `实例 ${config.instance} 的资源停止失败`);
}

export async function removeResources(config) {
  await stopResources(config);
  const containers = identifiers(await capture('docker', ['ps', '-aq', ...filters(config)], config.env));
  if (containers.length) await capture('docker', ['rm', '--volumes', ...containers], config.env);
  for (const type of ['volume', 'network']) {
    const names = identifiers(await capture('docker', [type, 'ls', '-q', ...filters(config)], config.env));
    if (names.length) await capture('docker', [type, 'rm', ...names], config.env);
  }
  await compose(config, ['down', '--volumes', '--timeout', '10']);
}

export async function resourceStatus(config) {
  return capture('docker', ['ps', '-a', ...filters(config), '--format', '{{.ID}}\t{{.Names}}\t{{.Status}}'], config.env);
}
