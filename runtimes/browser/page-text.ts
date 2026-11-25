import type { Page } from 'playwright';

export async function readPageText(page: Page, selector: string | null) {
  return page.evaluate(scope => {
    let element = document.body;
    if (scope !== null) {
      try {
        const matches = document.querySelectorAll<HTMLElement>(scope);
        if (matches.length !== 1) return { error: 'scope_unavailable' } as const;
        element = matches[0]!;
      } catch {
        return { error: 'invalid_scope' } as const;
      }
      const visibility = getComputedStyle(element).visibility;
      if (!element.getClientRects().length || visibility !== 'visible') {
        return { error: 'scope_unavailable' } as const;
      }
    }
    const title = document.title;
    const text = element?.innerText ?? '';
    return {
      title: title.slice(0, 512).toWellFormed(),
      title_truncated: title.length > 512,
      text: text.slice(0, 4096).toWellFormed(),
      text_truncated: text.length > 4096,
    };
  }, selector);
}
