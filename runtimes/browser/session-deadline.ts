export function readSessionDeadline(value: string | undefined, now = Date.now()) {
  if (value === undefined) return undefined;
  const match = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?Z$/.exec(value);
  if (!match || match[0] !== value) {
    throw new Error('CLOUDLET_BROWSER_DEADLINE must be an RFC 3339 UTC timestamp ending in Z');
  }
  const milliseconds = (match[2] ?? '').padEnd(3, '0').slice(0, 3);
  const normalized = `${match[1]}.${milliseconds}Z`;
  const timestamp = Date.parse(normalized);
  if (!Number.isFinite(timestamp) || new Date(timestamp).toISOString() !== normalized) {
    throw new Error('CLOUDLET_BROWSER_DEADLINE must be an RFC 3339 UTC timestamp ending in Z');
  }
  if (timestamp <= now) {
    throw new Error('CLOUDLET_BROWSER_DEADLINE must be in the future');
  }
  return timestamp;
}
