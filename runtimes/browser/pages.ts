import { randomUUID } from 'node:crypto';
import type { BrowserContext, Page } from 'playwright';

export type PageState = { page: Page; hasPage: boolean };

export const pageIDPattern = /^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/u;
const additionalPageLimit = 7;
const maxTitleUnits = 512;
const maxURLBytes = 4096;

export function limitPageTitle(value: string) {
  return value.slice(0, maxTitleUnits).toWellFormed();
}

function pageInfo(id: string, page: Page, title: string) {
  const address = Buffer.from(page.url(), 'utf8');
  const urlTruncated = address.length > maxURLBytes;
  const url = urlTruncated
    ? address.subarray(0, maxURLBytes).toString('utf8').replace(/\uFFFD$/u, '').replace(/%[0-9a-f]?$/iu, '')
    : address.toString('utf8');
  return { id, url, url_truncated: urlTruncated, title: limitPageTitle(title), title_truncated: title.length > maxTitleUnits };
}

export function sessionPages(context: BrowserContext, attachConsole: (page: Page) => void) {
  const pages = new Map<string, PageState>();

  async function open() {
    if (pages.size >= additionalPageLimit) return { status: 409, body: { error: 'page_limit' } };
    const id = randomUUID();
    const page = await context.newPage();
    attachConsole(page);
    pages.set(id, { page, hasPage: false });
    page.once('close', () => pages.delete(id));
    return { status: 201, body: pageInfo(id, page, '') };
  }

  function get(id: string) {
    const state = pages.get(id);
    return state && !state.page.isClosed() ? state : undefined;
  }

  async function describe(id: string) {
    const state = get(id);
    if (!state) return { status: 404, body: { error: 'page_not_found' } };
    return { status: 200, body: pageInfo(id, state.page, await state.page.title()) };
  }

  async function close(id: string) {
    const state = get(id);
    if (!state) return { status: 404, body: { error: 'page_not_found' } };
    await state.page.close();
    pages.delete(id);
    return { status: 204, body: {} };
  }

  return { open, get, describe, close };
}
