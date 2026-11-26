import type { Page } from 'playwright';
import { devicePixelsFit } from './viewport.ts';

export async function capturePageScreenshot(page: Page, fullPage: boolean, scale: number): Promise<Buffer | null> {
  if (!fullPage) return page.screenshot({ type: 'png', timeout: 0 });
  const size = await page.evaluate(() => {
    const root = document.documentElement;
    const body = document.body;
    return {
      width: Math.max(root.scrollWidth, root.offsetWidth, root.clientWidth,
        body?.scrollWidth ?? 0, body?.offsetWidth ?? 0, body?.clientWidth ?? 0),
      height: Math.max(root.scrollHeight, root.offsetHeight, root.clientHeight,
        body?.scrollHeight ?? 0, body?.offsetHeight ?? 0, body?.clientHeight ?? 0),
    };
  });
  if (!devicePixelsFit(size.width, size.height, scale)) return null;

  // Bound the capture rectangle even if layout grows after measurement.
  return page.screenshot({ type: 'png', fullPage: true, timeout: 0, clip: { x: 0, y: 0, ...size } });
}
