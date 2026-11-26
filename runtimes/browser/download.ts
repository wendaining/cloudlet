import { readFile, rm, stat } from 'node:fs/promises';
import { join } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import type { Browser, Page } from 'playwright';

const maxDownloadBytes = 8 * 1024 * 1024;
const guidPattern = /^[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}$/iu;

type DownloadResult = { status: 200; name: string; data: Buffer } | { status: 422 | 504 };
type Begin = { frameId: string; guid: string; suggestedFilename: string };
type Progress = { guid: string; state: string; receivedBytes: number; totalBytes: number };

export async function captureDownload(browser: Browser, page: Page, directory: string, selector: string, timeout: number): Promise<DownloadResult> {
  const target = await page.context().newCDPSession(page);
  let frameID: string;
  let contextID: string | undefined;
  try {
    const tree = await target.send('Page.getFrameTree');
    frameID = tree.frameTree.frame.id;
    const info = await target.send('Target.getTargetInfo');
    contextID = info.targetInfo.browserContextId;
  } finally {
    await target.detach();
  }
  if (!contextID) throw new Error('Chromium download context is missing');
  const cdp = await browser.newBrowserCDPSession();
  const completed = Promise.withResolvers<'complete' | 'rejected'>();
  const files = new Set<string>();
  const timer = new AbortController();
  const cancellation: Promise<unknown>[] = [];
  let current: Begin | undefined;
  let oversize = false;
  const cancel = (guid: string) => {
    cancellation.push(cdp.send('Browser.cancelDownload', { guid, browserContextId: contextID }).catch(() => {
      // Chromium can finish or cancel between the progress event and this command.
      // The final size check and unlink still apply.
    }));
  };
  const begin = (event: Begin) => {
    if (!guidPattern.test(event.guid)) return;
    files.add(event.guid);
    if (!current && event.frameId === frameID) {
      current = event;
    } else {
      cancel(event.guid);
    }
  };
  const progress = (event: Progress) => {
    if (!current || event.guid !== current.guid) return;
    if (event.receivedBytes > maxDownloadBytes || event.totalBytes > maxDownloadBytes) {
      if (!oversize) {
        oversize = true;
        if (event.state === 'inProgress') cancel(event.guid);
      }
      completed.resolve('rejected');
    } else if (event.state === 'completed') {
      completed.resolve('complete');
    } else if (event.state === 'canceled') {
      completed.resolve('rejected');
    }
  };
  cdp.on('Browser.downloadWillBegin', begin);
  cdp.on('Browser.downloadProgress', progress);
  try {
    await cdp.send('Browser.setDownloadBehavior', {
      behavior: 'allowAndName', browserContextId: contextID, downloadPath: directory, eventsEnabled: true,
    });
    const clicked = page.locator('css=' + selector).click({ timeout }).catch(error => {
      if (!current) throw error;
    });
    const outcome = await Promise.race([
      delay(timeout, 'timeout' as const, { signal: timer.signal }),
      Promise.all([clicked, completed.promise]).then(([, status]) => status),
    ]);
    if (outcome === 'timeout') return { status: 504 };
    if (outcome !== 'complete' || !current) return { status: 422 };
    const file = join(directory, current.guid);
    if ((await stat(file)).size > maxDownloadBytes) return { status: 422 };
    const data = await readFile(file);
    if (data.length > maxDownloadBytes) return { status: 422 };
    return { status: 200, name: current.suggestedFilename, data };
  } finally {
    timer.abort();
    if (current) cancel(current.guid);
    await Promise.all(cancellation);
    cdp.off('Browser.downloadWillBegin', begin);
    cdp.off('Browser.downloadProgress', progress);
    try {
      await cdp.detach();
    } finally {
      for (const id of files) {
        await rm(join(directory, id), { force: true });
        await rm(join(directory, id+'.crdownload'), { force: true });
      }
    }
  }
}
