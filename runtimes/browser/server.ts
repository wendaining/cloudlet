import { timingSafeEqual } from 'node:crypto';
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import { chromium, errors } from 'playwright';

const token = process.env.CLOUDLET_BROWSER_TOKEN;
if (!token || !/^[A-Za-z0-9_-]{32,}$/.test(token)) {
  throw new Error('CLOUDLET_BROWSER_TOKEN must contain at least 32 URL-safe characters');
}

const credential = Buffer.from(`Bearer ${token}`);
const maxBodyBytes = 8192;
const defaultTimeout = 10000;
const maxTimeout = 30000;

function send(response: ServerResponse, status: number, body: object) {
  response.writeHead(status, {
    'Content-Type': 'application/json',
    'Cache-Control': 'no-store',
  }).end(JSON.stringify(body));
}

async function readNavigation(request: IncomingMessage) {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of request.iterator({ destroyOnReturn: false })) {
    size += chunk.length;
    if (size > maxBodyBytes) return { error: 'body_too_large', status: 413 } as const;
    chunks.push(chunk);
  }

  let input: unknown;
  try {
    input = JSON.parse(Buffer.concat(chunks).toString('utf8'));
  } catch {
    return { error: 'invalid_json', status: 400 } as const;
  }
  if (input === null || typeof input !== 'object' || !('url' in input) || typeof input.url !== 'string') {
    return { error: 'invalid_url', status: 400 } as const;
  }
  const url = URL.parse(input.url);
  if (!url || !['http:', 'https:'].includes(url.protocol) || url.username || url.password) {
    return { error: 'invalid_url', status: 400 } as const;
  }
  const timeout = 'timeout_ms' in input ? input.timeout_ms : defaultTimeout;
  if (typeof timeout !== 'number' || !Number.isInteger(timeout) || timeout < 1 || timeout > maxTimeout) {
    return { error: 'invalid_timeout', status: 400 } as const;
  }
  return { url: url.href, timeout };
}

const browser = await chromium.launch({ chromiumSandbox: true, handleSIGTERM: false, handleSIGINT: false });
let stopping = false;
let navigating = false;
const server = createServer({ requestTimeout: 15000, headersTimeout: 5000 }, (request, response) => {
  void handle(request, response).catch((error: unknown) => {
    if (response.destroyed) return;
    if (response.headersSent) {
      response.destroy();
      return;
    }
    if (error instanceof errors.TimeoutError) {
      send(response, 504, { error: 'navigation_timeout' });
      return;
    }
    send(response, 502, { error: 'navigation_failed' });
  });
});

async function shutdown(code: number) {
  if (stopping) return;
  stopping = true;
  process.exitCode = code;
  server.close();
  server.closeAllConnections();
  try {
    await browser.close();
  } catch {
    process.exitCode = 1;
  }
}

for (const signal of ['SIGTERM', 'SIGINT'] as const) {
  process.on(signal, () => void shutdown(0));
}
browser.on('disconnected', () => void shutdown(1));

const page = await browser.newPage().catch(async (error: unknown) => {
  await shutdown(1);
  throw error;
});

async function handle(request: IncomingMessage, response: ServerResponse) {
  if (request.method === 'GET' && request.url === '/readyz') {
    send(response, 200, { status: 'ready' });
    return;
  }
  if (request.method !== 'POST' || request.url !== '/navigate') {
    send(response, 404, { error: 'not_found' });
    return;
  }

  const provided = Buffer.from(request.headers.authorization ?? '');
  if (provided.length !== credential.length || !timingSafeEqual(provided, credential)) {
    response.setHeader('WWW-Authenticate', 'Bearer');
    send(response, 401, { error: 'unauthorized' });
    return;
  }
  const navigation = await readNavigation(request);
  if (navigation.error) {
    if (navigation.status === 413) {
      response.setHeader('Connection', 'close');
      request.resume();
    }
    send(response, navigation.status, { error: navigation.error });
    return;
  }
  if (navigating) {
    send(response, 409, { error: 'navigation_in_progress' });
    return;
  }

  navigating = true;
  try {
    await page.goto(navigation.url, { timeout: navigation.timeout, waitUntil: 'domcontentloaded' });
    send(response, 200, { url: page.url(), title: await page.title() });
  } finally {
    navigating = false;
  }
}

server.on('error', (error) => {
  console.error('Browser Run listener failed:', error);
  void shutdown(1);
});
if (!stopping) server.listen(8080, '0.0.0.0');
