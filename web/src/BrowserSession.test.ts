import { describe, expect, it } from 'vitest';
import { browserNavigationURL } from './BrowserSession.rules';

describe('browserNavigationURL', () => {
  it('trims the address and preserves the path, query and fragment', () => {
    const url = 'https://example.test:8080/文档?q=1#目录';
    expect(browserNavigationURL('  ' + url + '  ')).toBe(url);
    expect(browserNavigationURL('http://[::1]:8080/path')).toBe(
      'http://[::1]:8080/path',
    );
  });

  it('measures the limit in UTF-8 bytes', () => {
    const prefix = 'https://example.test/';
    const boundary =
      prefix + '路'.repeat(1358) + 'a'.repeat(4096 - prefix.length - 1358 * 3);
    expect(browserNavigationURL(boundary)).toBe(boundary);
    expect(() => browserNavigationURL(boundary + 'a')).toThrow('4096');
  });

  it.each([undefined, '', '  '])('rejects a missing address (%s)', (value) => {
    expect(() => browserNavigationURL(value)).toThrow('请输入导航地址');
  });

  it.each([
    'example.test',
    'https:///example.test',
    'https://example.test:bad',
    'https://user:password@example.test/',
    'http://@example.test/',
    'http://example.test\\elsewhere',
    'javascript:alert(1)',
    'file:///tmp/file',
  ])('rejects an invalid or credentialed address (%s)', (value) => {
    expect(() => browserNavigationURL(value)).toThrow('不带凭据');
  });
});
