import { randomUUID } from 'node:crypto';
import type { BrowserContext, Page } from 'playwright';

export type PageState = { page: Page; hasPage: boolean };

export const pageIDPattern = /^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/u;
const additionalPageLimit = 7;

export function sessionPages(context: BrowserContext) {
  const pages = new Map<string, PageState>();

  async function open() {
    if (pages.size >= additionalPageLimit) return { status: 409, body: { error: 'page_limit' } };
    const id = randomUUID();
    const page = await context.newPage();
    pages.set(id, { page, hasPage: false });
    page.once('close', () => pages.delete(id));
    return { status: 201, body: { id, url: page.url(), title: '' } };
  }

  function get(id: string) {
    const state = pages.get(id);
    return state && !state.page.isClosed() ? state : undefined;
  }

  async function describe(id: string) {
    const state = get(id);
    if (!state) return { status: 404, body: { error: 'page_not_found' } };
    return { status: 200, body: { id, url: state.page.url(), title: await state.page.title() } };
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
