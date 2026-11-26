import type { Page } from 'playwright';

export type WaitCondition = { condition: 'ready' } | { condition: 'text'; text: string };

export function parseWaitCondition(input: object): WaitCondition | undefined {
  if (!('condition' in input)) return undefined;
  if (input.condition === 'ready' && !('text' in input)) return { condition: 'ready' };
  if (input.condition === 'text' && 'text' in input && typeof input.text === 'string' &&
      input.text.trim() && input.text.length <= 1024 && !input.text.includes('\0')) {
    return { condition: 'text', text: input.text };
  }
  return undefined;
}

export async function waitForPage(page: Page, condition: WaitCondition, timeout: number) {
  const matched = await page.waitForFunction(expected => {
    if (expected.condition === 'ready') return document.readyState === 'complete';
    const body = document.body;
    return body?.checkVisibility({ visibilityProperty: true }) &&
      (body.innerText ?? '').includes(expected.text);
  }, condition, { timeout, polling: 100 });
  await matched.dispose();
}
