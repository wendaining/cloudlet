import type { StaticSiteActivePublication, StaticSiteSummary } from './client';

export type SitePublicationState =
  | { kind: 'loading' }
  | { kind: 'unpublished' }
  | { kind: 'error'; message: string }
  | {
      kind: 'published';
      versionId: string;
      hostname: string;
      url: string | null;
      revision: string;
    };

export type StaticSiteRow = {
  id: string;
  name: string;
  createdAt: string;
  publication: SitePublicationState;
};

export function safePublicSiteUrl(rawUrl: string): string | null {
  try {
    const parsed = new URL(rawUrl);
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
      return null;
    }
    return parsed.toString();
  } catch {
    return null;
  }
}

export function resolveSitePublicationState(input: {
  isPending: boolean;
  error: Error | null;
  data?: StaticSiteActivePublication | null;
}): SitePublicationState {
  if (input.isPending) {
    return { kind: 'loading' };
  }
  if (input.error) {
    return { kind: 'error', message: input.error.message };
  }
  if (!input.data) {
    return { kind: 'unpublished' };
  }
  return {
    kind: 'published',
    versionId: input.data.version_id,
    hostname: input.data.hostname,
    url: safePublicSiteUrl(input.data.url),
    revision: input.data.revision,
  };
}

export function buildStaticSiteRows(
  sites: StaticSiteSummary[],
  states: SitePublicationState[],
): StaticSiteRow[] {
  return sites.map((site, index) => ({
    id: site.id,
    name: site.name,
    createdAt: site.created_at,
    publication: states[index] ?? { kind: 'loading' },
  }));
}
