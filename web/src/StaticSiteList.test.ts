import { describe, expect, test } from 'vitest';
import {
  buildStaticSiteRows,
  resolveSitePublicationState,
  safePublicSiteUrl,
} from './StaticSiteList.rules';

describe('safePublicSiteUrl', () => {
  test('accepts http and https URLs', () => {
    expect(safePublicSiteUrl('http://docs.localhost:8081/')).toBe(
      'http://docs.localhost:8081/',
    );
    expect(safePublicSiteUrl('https://docs.example.com/')).toBe(
      'https://docs.example.com/',
    );
  });

  test('rejects non-http protocols and malformed URLs', () => {
    expect(safePublicSiteUrl('javascript:alert(1)')).toBeNull();
    expect(safePublicSiteUrl('data:text/html,hi')).toBeNull();
    expect(safePublicSiteUrl('not-a-url')).toBeNull();
  });
});

describe('resolveSitePublicationState', () => {
  test('returns loading while pending', () => {
    expect(
      resolveSitePublicationState({ isPending: true, error: null }),
    ).toEqual({ kind: 'loading' });
  });

  test('returns error when query fails', () => {
    expect(
      resolveSitePublicationState({
        isPending: false,
        error: new Error('站点发布未配置'),
      }),
    ).toEqual({ kind: 'error', message: '站点发布未配置' });
  });

  test('returns unpublished when publication is null or undefined', () => {
    expect(
      resolveSitePublicationState({
        isPending: false,
        error: null,
        data: null,
      }),
    ).toEqual({ kind: 'unpublished' });
  });

  test('returns published with sanitized url', () => {
    expect(
      resolveSitePublicationState({
        isPending: false,
        error: null,
        data: {
          site_id: '11111111-1111-1111-1111-111111111111',
          version_id: '22222222-2222-2222-2222-222222222222',
          hostname: 'docs.localhost',
          url: 'http://docs.localhost:8081/',
          revision: '2026-11-28T00:00:00Z',
        },
      }),
    ).toEqual({
      kind: 'published',
      versionId: '22222222-2222-2222-2222-222222222222',
      hostname: 'docs.localhost',
      url: 'http://docs.localhost:8081/',
      revision: '2026-11-28T00:00:00Z',
    });
  });
});

describe('buildStaticSiteRows', () => {
  test('combines sites with publication states', () => {
    const rows = buildStaticSiteRows(
      [
        {
          id: 'site-1',
          name: 'docs',
          created_at: '2026-11-28T00:00:00Z',
        },
        {
          id: 'site-2',
          name: 'blog',
          created_at: '2026-11-28T01:00:00Z',
        },
      ],
      [{ kind: 'unpublished' }],
    );

    expect(rows).toEqual([
      {
        id: 'site-1',
        name: 'docs',
        createdAt: '2026-11-28T00:00:00Z',
        publication: { kind: 'unpublished' },
      },
      {
        id: 'site-2',
        name: 'blog',
        createdAt: '2026-11-28T01:00:00Z',
        publication: { kind: 'loading' },
      },
    ]);
  });
});
