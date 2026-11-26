import type { Page } from 'playwright';

const maxEntries = 100;
const maxMessageLength = 512;

export type ConsoleEntry = {
  kind: 'console' | 'page_error';
  level: string;
  message: string;
  truncated: boolean;
};

export function collectConsole() {
  const entries: ConsoleEntry[] = [];
  let dropped = 0;

  function add(kind: ConsoleEntry['kind'], level: string, text: string) {
    if (entries.length === maxEntries) {
      entries.shift();
      dropped++;
    }
    entries.push({
      kind, level,
      message: text.slice(0, maxMessageLength).toWellFormed(),
      truncated: text.length > maxMessageLength,
    });
  }

  function attach(page: Page) {
    page.on('console', message => add('console', message.type(), message.text()));
    page.on('pageerror', error => add('page_error', 'error', error.message));
  }

  return { attach, read: () => ({ entries, dropped }) };
}
