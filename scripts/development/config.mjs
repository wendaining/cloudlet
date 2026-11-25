import { createHash, randomBytes } from 'node:crypto';
import { appendFile, readFile, writeFile, mkdir, realpath } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { parse } from 'dotenv';
import { z } from 'zod';

export const root = fileURLToPath(new URL('../../', import.meta.url));
const port = z.coerce.number().int().min(1024).max(65535);
const schema = z.object({
  CLOUDLET_ENV: z.literal('development'),
  CLOUDLET_INSTANCE: z.string().regex(/^[a-z][a-z0-9-]{0,31}$/u),
  CLOUDLET_API_PORT: port,
  CLOUDLET_GATEWAY_PORT: port,
  CLOUDLET_WEB_PORT: port,
  CLOUDLET_DEBUG_PORT: port,
  CLOUDLET_DATABASE_URL: z.url(),
  CLOUDLET_SECRET_KEY: z.string().regex(/^[0-9a-f]{64}$/u),
  CLOUDLET_DEV_EMAIL: z.email(),
  CLOUDLET_DEV_PASSWORD: z.string().min(16),
  DOCKER_HOST: z.string().optional(),
  DOCKER_CONTEXT: z.string().optional(),
}).strict();

export async function initialize(file = process.env.ENV_FILE ?? '.env.local') {
  const target = path.resolve(root, file);
  const template = await readFile(path.join(root, '.env.example'), 'utf8');
  const source = template
    .replaceAll('GENERATED_DATABASE_PASSWORD', randomBytes(24).toString('hex'))
    .replaceAll('GENERATED_SECRET_KEY', randomBytes(32).toString('hex'))
    .replaceAll('GENERATED_ACCOUNT_PASSWORD', randomBytes(24).toString('hex'));
  try {
    await writeFile(target, source, { flag: 'wx', mode: 0o600 });
    console.log(`已创建开发配置：${target}`);
  } catch (error) {
    if (error.code !== 'EEXIST') throw error;
    const existing = await readFile(target, 'utf8');
    if (!/^CLOUDLET_SECRET_KEY=/mu.test(existing)) {
      const separator = existing.endsWith('\n') ? '' : '\n';
      await appendFile(target, `${separator}CLOUDLET_SECRET_KEY=${randomBytes(32).toString('hex')}\n`);
      console.log(`已补充平台密钥：${target}`);
    } else if (/^CLOUDLET_SECRET_KEY=GENERATED_SECRET_KEY$/mu.test(existing)) {
      await writeFile(target, existing.replace(
        /^CLOUDLET_SECRET_KEY=GENERATED_SECRET_KEY$/mu,
        `CLOUDLET_SECRET_KEY=${randomBytes(32).toString('hex')}`,
      ));
      console.log(`已生成平台密钥：${target}`);
    }
    console.log(`保留已有开发配置：${target}`);
  }
}

export async function loadConfig(file = process.env.ENV_FILE ?? '.env.local') {
  const filename = await realpath(path.resolve(root, file));
  const values = parse(await readFile(filename));
  const overrides = Object.fromEntries(Object.keys(schema.shape)
    .filter(key => process.env[key] !== undefined).map(key => [key, process.env[key]]));
  const result = schema.safeParse({ ...values, ...overrides });
  if (!result.success) {
    throw new Error(`开发配置无效：${result.error.issues.map(issue => `${issue.path.join('.')}: ${issue.message}`).join('，')}`);
  }
  const env = result.data;
  if (env.DOCKER_HOST && env.DOCKER_CONTEXT) throw new Error('DOCKER_HOST 与 DOCKER_CONTEXT 同时设置');
  if (env.CLOUDLET_DATABASE_URL.includes('GENERATED_') || env.CLOUDLET_SECRET_KEY.includes('GENERATED_') || env.CLOUDLET_DEV_PASSWORD.includes('GENERATED_')) {
    throw new Error('请在本地配置中设置实际凭据，或者使用 make setup 生成配置');
  }
  const database = new URL(env.CLOUDLET_DATABASE_URL);
  if (database.protocol !== 'postgres:' || database.hostname !== '127.0.0.1'
    || !database.port || database.searchParams.get('sslmode') !== 'disable'
    || !/^[a-z][a-z0-9_]*$/u.test(database.username)
    || !/^\/[a-z][a-z0-9_]*$/u.test(database.pathname)
    || decodeURIComponent(database.password).length < 16) {
    throw new Error('CLOUDLET_DATABASE_URL 需要完整的本机 PostgreSQL 地址、独立端口、账号、密码、数据库与 sslmode=disable');
  }
  const databasePort = port.parse(database.port);
  const ports = [env.CLOUDLET_API_PORT, env.CLOUDLET_GATEWAY_PORT, env.CLOUDLET_WEB_PORT, env.CLOUDLET_DEBUG_PORT, databasePort];
  if (new Set(ports).size !== ports.length) throw new Error('本实例的监听端口不能重复');
  const instance = env.CLOUDLET_INSTANCE;
  const directory = path.join(root, '.local', instance);
  const images = JSON.parse(await readFile(path.join(root, 'configs/images.json'), 'utf8'));
  const environment = {
    ...process.env,
    ...Object.fromEntries(Object.entries(env).map(([key, value]) => [key, String(value)])),
    CLOUDLET_POSTGRES_IMAGE: images.postgres,
    CLOUDLET_DATABASE_PORT: String(databasePort),
    CLOUDLET_POSTGRES_USER: decodeURIComponent(database.username),
    CLOUDLET_POSTGRES_PASSWORD: decodeURIComponent(database.password),
    CLOUDLET_POSTGRES_DB: database.pathname.slice(1),
    CLOUDLET_API_UPSTREAM: `http://127.0.0.1:${env.CLOUDLET_API_PORT}`,
    CLOUDLET_PUBLIC_ORIGIN: `http://127.0.0.1:${env.CLOUDLET_WEB_PORT}`,
    CLOUDLET_GATEWAY_PUBLIC_URL: `http://localhost:${env.CLOUDLET_GATEWAY_PORT}`,
    CLOUDLET_RUNTIME_ACCESS: 'published',
    CLOUDLET_RUNTIME_NETWORK: `cloudlet-${instance}-execution`,
    GIN_MODE: 'release',
    COMPOSE_PROGRESS: 'plain',
    CLOUDLET_CONFIG: path.join(directory, 'server.json'),
    CLOUDLET_DEV_DIRECTORY: directory,
    COMPOSE_PROJECT_NAME: `cloudlet-${instance}`,
  };
  return {
    env: environment, instance, directory, filename, ports, databasePort,
    api: environment.CLOUDLET_API_UPSTREAM,
    web: environment.CLOUDLET_PUBLIC_ORIGIN,
    gateway: `http://127.0.0.1:${env.CLOUDLET_GATEWAY_PORT}`,
    controlPath: path.join('/tmp', `cl-${createHash('sha256').update(`${process.getuid()}:${instance}`).digest('hex').slice(0, 16)}`),
  };
}

export async function writeServerConfig(config) {
  await mkdir(config.directory, { recursive: true, mode: 0o700 });
  await writeFile(config.env.CLOUDLET_CONFIG, JSON.stringify({
    environment: 'development',
    instance: config.instance,
    public_origin: config.web,
    site_storage_directory: path.join(config.directory, 'site-storage'),
    api_address: `127.0.0.1:${config.env.CLOUDLET_API_PORT}`,
    gateway_address: `127.0.0.1:${config.env.CLOUDLET_GATEWAY_PORT}`,
    routes: [{ host: 'demo.localhost', target: config.api }],
    workers: {
      directory: path.join(config.directory, 'workers'),
      host_id: config.instance,
      host_budget: { cpu_millis: 4000, memory_bytes: 4294967296, instances: 4 },
      project_quota: { cpu_millis: 4000, memory_bytes: 4294967296, instances: 4 },
    },
    containers: {
      host_id: config.instance,
      host_budget: { cpu_millis: 4000, memory_bytes: 4294967296, instances: 4 },
      project_quota: { cpu_millis: 4000, memory_bytes: 4294967296, instances: 4 },
    },
  }, null, 2) + '\n', { mode: 0o600 });
}
