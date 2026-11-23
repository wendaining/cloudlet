export function readSessionDeadline(value: string | undefined, now = Date.now()) {
  if (value === undefined) return undefined;
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp)) {
    throw new Error('CLOUDLET_BROWSER_DEADLINE must be an ISO 8601 UTC timestamp');
  }
  const canonical = new Date(timestamp).toISOString();
  if (value !== canonical && value !== canonical.replace('.000Z', 'Z')) {
    throw new Error('CLOUDLET_BROWSER_DEADLINE must be an ISO 8601 UTC timestamp');
  }
  if (timestamp <= now) {
    throw new Error('CLOUDLET_BROWSER_DEADLINE must be in the future');
  }
  return timestamp;
}
