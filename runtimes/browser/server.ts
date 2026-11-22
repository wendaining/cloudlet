import { timingSafeEqual } from 'node:crypto';
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import { setTimeout as delay } from 'node:timers/promises';
import { chromium } from 'playwright';

const token = process.env.CLOUDLET_BROWSER_TOKEN;
if (!token || !/^[A-Za-z0-9_-]{32,}$/.test(token)) {
  throw new Error('CLOUDLET_BROWSER_TOKEN must contain at least 32 URL-safe characters');
}

const credential = Buffer.from(`Bearer ${token}`);
const maxBodyBytes = 8192;
const defaultTimeout = 10000;
const maxTimeout = 30000;
const maxScreenshotBytes = 2 * 1024 * 1024;
const viewport = { width: 1280, height: 720 };

function send(response: ServerResponse, status: number, body: object) {
  response.writeHead(status, {
    'Content-Type': 'application/json',
    'Cache-Control': 'no-store',
  }).end(JSON.stringify(body));
}

async function readCommand(request: IncomingMessage, screenshot: boolean) {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of request.iterator({ destroyOnReturn: false })) {
    size += chunk.length;
    if (size > maxBodyBytes) return { error: 'body_too_large', status: 413 } as const;
    chunks.push(chunk);
  }

  let input: unknown;
  try {
    input = screenshot && size === 0 ? {} : JSON.parse(Buffer.concat(chunks).toString('utf8'));
  } catch {
    return { error: 'invalid_json', status: 400 } as const;
  }
  if (input === null || typeof input !== 'object' || Array.isArray(input)) {
    return { error: screenshot ? 'invalid_json' : 'invalid_url', status: 400 } as const;
  }
  let url: URL | undefined;
  if (!screenshot) {
    const target = 'url' in input && typeof input.url === 'string' ? URL.parse(input.url) : null;
    if (!target || !isWebURL(target.href) || target.username || target.password) {
      return { error: 'invalid_url', status: 400 } as const;
    }
    url = target;
  }
  const timeout = 'timeout_ms' in input ? input.timeout_ms : defaultTimeout;
  if (typeof timeout !== 'number' || !Number.isInteger(timeout) || timeout < 1 || timeout > maxTimeout) {
    return { error: 'invalid_timeout', status: 400 } as const;
  }
  return { url, timeout };
}

function isWebURL(url: string) {
  return url.startsWith('http:') || url.startsWith('https:');
}

const browser = await chromium.launch({ chromiumSandbox: true, handleSIGTERM: false, handleSIGINT: false });
let stopping = false;
let activeCommand: 'navigation' | 'screenshot' | undefined;
let hasPage = false;
const server = createServer({ requestTimeout: 15000, headersTimeout: 5000 }, (request, response) => {
  void handle(request, response).catch(() => {
    if (response.destroyed) return;
    if (response.headersSent) {
      response.destroy();
      return;
    }
    send(response, 502, { error: request.url === '/screenshot' ? 'screenshot_failed' : 'navigation_failed' });
  });
});

async function shutdown(code: number) {
  if (stopping) return;
  stopping = true;
  process.exitCode = code;
  server.close();
  server.closeAllConnections();
  const forceExit = setTimeout(() => process.exit(1), 5000).unref();
  try {
    await browser.close();
  } catch {
    process.exitCode = 1;
  } finally {
    clearTimeout(forceExit);
  }
}

for (const signal of ['SIGTERM', 'SIGINT'] as const) {
  process.on(signal, () => void shutdown(0));
}
browser.on('disconnected', () => void shutdown(1));

const page = await browser.newPage({ viewport, deviceScaleFactor: 1 }).catch(async (error: unknown) => {
  await shutdown(1);
  throw error;
});

async function navigate(url: URL) {
  hasPage = false;
  await page.goto(url.href, { timeout: 0, waitUntil: 'domcontentloaded' });
  const title = await page.title();
  const currentURL = page.url();
  hasPage = isWebURL(currentURL);
  return { url: currentURL, title };
}

async function handle(request: IncomingMessage, response: ServerResponse) {
  if (request.method === 'GET' && request.url === '/readyz') {
    send(response, 200, { status: 'ready' });
    return;
  }
  const screenshot = request.url === '/screenshot';
  if (request.method !== 'POST' || (!screenshot && request.url !== '/navigate')) {
    send(response, 404, { error: 'not_found' });
    return;
  }

  const provided = Buffer.from(request.headers.authorization ?? '');
  if (provided.length !== credential.length || !timingSafeEqual(provided, credential)) {
    response.setHeader('WWW-Authenticate', 'Bearer');
    send(response, 401, { error: 'unauthorized' });
    return;
  }
  const command = await readCommand(request, screenshot);
  if (command.error) {
    if (command.status === 413) {
      response.setHeader('Connection', 'close');
      request.resume();
    }
    send(response, command.status, { error: command.error });
    return;
  }
  if (activeCommand) {
    send(response, 409, { error: `${activeCommand}_in_progress` });
    return;
  }
  if (screenshot && (!hasPage || page.isClosed() || !isWebURL(page.url()))) {
    send(response, 409, { error: 'no_page' });
    return;
  }

  activeCommand = screenshot ? 'screenshot' : 'navigation';
  let expired = false;
  const timer = new AbortController();
  try {
    const result = await Promise.race([
      delay(command.timeout, undefined, { signal: timer.signal }),
      command.url ? navigate(command.url) : page.screenshot({ type: 'png', fullPage: false, timeout: 0 }),
    ]);
    if (result) {
      if (!Buffer.isBuffer(result)) {
        send(response, 200, result);
      } else if (result.length > maxScreenshotBytes) {
        send(response, 422, { error: 'screenshot_too_large' });
      } else {
        response.writeHead(200, {
          'Content-Type': 'image/png',
          'Content-Length': result.length,
          'Cache-Control': 'no-store',
        }).end(result);
      }
      return;
    }

    expired = true;
    // A stalled renderer can still be busy after the deadline. Retire this session after reporting 504.
    if (response.destroyed) {
      void shutdown(0);
      return;
    }
    response.once('close', () => void shutdown(0));
    response.setHeader('Connection', 'close');
    send(response, 504, { error: screenshot ? 'screenshot_timeout' : 'navigation_timeout' });
  } finally {
    timer.abort();
    if (!expired) activeCommand = undefined;
  }
}

server.on('error', (error) => {
  console.error('Browser Run listener failed:', error);
  void shutdown(1);
});
if (!stopping) server.listen(8080, '0.0.0.0');
