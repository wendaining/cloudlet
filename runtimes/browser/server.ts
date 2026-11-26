import { timingSafeEqual } from 'node:crypto';
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { chromium, errors, type Page } from 'playwright';
import { readSessionDeadline } from './session-deadline.ts';
import { readViewport } from './viewport.ts';
import { captureDownload } from './download.ts';
import { collectConsole } from './console-history.ts';

const token = process.env.CLOUDLET_BROWSER_TOKEN;
if (!token || !/^[A-Za-z0-9_-]{32,}$/.test(token)) {
  throw new Error('CLOUDLET_BROWSER_TOKEN must contain at least 32 URL-safe characters');
}

const credential = Buffer.from(`Bearer ${token}`);
const sessionDeadline = readSessionDeadline(process.env.CLOUDLET_BROWSER_DEADLINE);
const maxBodyBytes = 8192;
const defaultTimeout = 10000;
const maxTimeout = 30000;
const maxScreenshotBytes = 2 * 1024 * 1024;
const viewportOptions = readViewport(process.env);

function send(response: ServerResponse, status: number, body: object) {
  response.writeHead(status, {
    'Content-Type': 'application/json',
    'Cache-Control': 'no-store',
  }).end(JSON.stringify(body));
}

async function readCommand(request: IncomingMessage, screenshot: boolean, click: boolean, form: boolean) {
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
  let selector: string | undefined;
  let formAction: { action: 'fill' | 'select'; value: string } | undefined;
  if (click || form) {
    if (!('selector' in input) || typeof input.selector !== 'string' ||
        !input.selector.trim() || input.selector.length > 1024 || /\p{Cc}/u.test(input.selector)) {
      return { error: 'invalid_selector', status: 400 } as const;
    }
    selector = input.selector;
    if (form) {
      if (!('action' in input) || (input.action !== 'fill' && input.action !== 'select') ||
          !('value' in input) || typeof input.value !== 'string' || input.value.length > 1024 || input.value.includes('\0')) {
        return { error: 'invalid_form_action', status: 400 } as const;
      }
      formAction = { action: input.action, value: input.value };
    }
  } else if (!screenshot) {
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
  return { url, selector, formAction, timeout };
}

function isWebURL(url: string) {
  return url.startsWith('http:') || url.startsWith('https:');
}

const downloadsPath = mkdtempSync(join(tmpdir(), 'cloudlet-downloads-'));
process.once('exit', () => rmSync(downloadsPath, { recursive: true, force: true }));
const launchTimeout = 30000;
const remainingAtLaunch = sessionDeadline === undefined ? Infinity : sessionDeadline - Date.now();
const browser = await chromium.launch({
  downloadsPath, chromiumSandbox: true, handleSIGTERM: false, handleSIGINT: false,
  timeout: Math.min(launchTimeout, Math.max(1, remainingAtLaunch)),
}).catch((error: unknown) => {
  if (error instanceof errors.TimeoutError && remainingAtLaunch <= launchTimeout &&
      sessionDeadline !== undefined && Date.now() >= sessionDeadline) {
    process.exit(0);
  }
  throw error;
});
let stopping = false;
let expiryTimer: ReturnType<typeof setTimeout> | undefined;
let activeCommand: 'navigation' | 'screenshot' | 'click' | 'form' | 'download' | undefined;
let hasPage = false;
const server = createServer({ requestTimeout: 15000, headersTimeout: 5000 }, (request, response) => {
  void handle(request, response).catch(() => {
    if (response.destroyed) return;
    if (response.headersSent) {
      response.destroy();
      return;
    }
    const kind = request.url === '/viewport' ? 'viewport' : request.url === '/screenshot' ? 'screenshot' : 'navigation';
    send(response, 502, { error: `${kind}_failed` });
  });
});

async function shutdown(code: number) {
  if (stopping) return;
  stopping = true;
  clearTimeout(expiryTimer);
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

function checkExpiry() {
  if (stopping || sessionDeadline === undefined) return;
  clearTimeout(expiryTimer);
  const remaining = sessionDeadline - Date.now();
  if (remaining <= 0) {
    void shutdown(0);
    return;
  }
  // Recheck wall time so clock adjustments cannot extend the deadline indefinitely.
  expiryTimer = setTimeout(checkExpiry, Math.min(remaining, 1000)).unref();
}

checkExpiry();
let page: Page;
let readConsole: ReturnType<typeof collectConsole>;
if (!stopping) {
  try {
    page = await browser.newPage(viewportOptions);
    readConsole = collectConsole(page);
  } catch (error) {
    if (!stopping) {
      await shutdown(1);
      throw error;
    }
  }
}

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
  const click = request.url === '/click';
  const download = request.url === '/download';
  const form = request.url === '/form';
  const viewport = request.method === 'GET' && request.url === '/viewport';
  const consoleHistory = request.method === 'GET' && request.url === '/console';
  if (!viewport && !consoleHistory && (request.method !== 'POST' || (!screenshot && !click && !form && !download && request.url !== '/navigate'))) {
    send(response, 404, { error: 'not_found' });
    return;
  }

  const provided = Buffer.from(request.headers.authorization ?? '');
  if (provided.length !== credential.length || !timingSafeEqual(provided, credential)) {
    response.setHeader('WWW-Authenticate', 'Bearer');
    send(response, 401, { error: 'unauthorized' });
    return;
  }
  if (consoleHistory) {
    send(response, 200, readConsole());
    return;
  }
  const command = viewport ? undefined : await readCommand(request, screenshot, click || download, form);
  if (command?.error) {
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
  if (!command) {
    const size = page.viewportSize();
    if (!size || page.isClosed()) {
      send(response, 502, { error: 'viewport_failed' });
      return;
    }
    send(response, 200, { ...size, device_scale_factor: viewportOptions.deviceScaleFactor });
    return;
  }
  if ((screenshot || click || form || download) && (!hasPage || page.isClosed() || !isWebURL(page.url()))) {
    send(response, 409, { error: 'no_page' });
    return;
  }

  if (command.selector !== undefined) {
    const kind = download ? 'download' : command.formAction ? 'form' : 'click';
    activeCommand = kind;
    try {
      if (download) {
        const result = await captureDownload(browser, page, downloadsPath, command.selector, command.timeout);
        if (result.status !== 200) {
          send(response, result.status, { error: result.status === 504 ? 'download_timeout' : 'download_rejected' });
        } else {
          response.writeHead(200, {
            'Content-Type': 'application/octet-stream',
            'Content-Length': result.data.length,
            'X-Cloudlet-Download-Name': encodeURIComponent(result.name.toWellFormed()),
            'Cache-Control': 'no-store',
          }).end(result.data);
        }
        return;
      }
      // Playwright enforces strict, actionable CSS selection and its own deadline.
      const target = page.locator('css=' + command.selector);
      const options = { timeout: command.timeout };
      if (command.formAction?.action === 'fill') {
        await target.fill(command.formAction.value, options);
      } else if (command.formAction?.action === 'select') {
        await target.selectOption({ value: command.formAction.value }, options);
      } else {
        await target.click(options);
      }
      response.writeHead(204, { 'Cache-Control': 'no-store' }).end();
    } catch (error: unknown) {
      if (error instanceof errors.TimeoutError) {
        send(response, 504, { error: `${kind}_timeout` });
      } else {
        send(response, 422, { error: `${kind}_failed` });
      }
    } finally {
      activeCommand = undefined;
    }
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
    send(response, 504, { error: `${activeCommand}_timeout` });
  } finally {
    timer.abort();
    if (!expired) activeCommand = undefined;
  }
}

server.on('error', (error) => {
  console.error('Browser Run listener failed:', error);
  void shutdown(1);
});
checkExpiry();
if (!stopping) server.listen(8080, '0.0.0.0');
