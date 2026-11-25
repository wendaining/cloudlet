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
    }
    const title = document.title;
    const rendered = element !== null && element.checkVisibility({ visibilityProperty: true });
    if (!rendered && scope !== null) {
      return { error: 'scope_unavailable' } as const;
    }
    // innerText falls back to textContent when the element is not rendered.
    const text = rendered ? (element.innerText ?? '') : '';
    return {
      title: title.slice(0, 512).toWellFormed(),
      title_truncated: title.length > 512,
      text: text.slice(0, 4096).toWellFormed(),
      text_truncated: text.length > 4096,
    };
  }, selector);
}
