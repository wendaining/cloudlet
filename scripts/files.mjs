import { readdir } from 'node:fs/promises';
import path from 'node:path';

const ignored = new Set(['.git', 'node_modules', '.local', 'bin', 'dist']);

export async function files(directory = '.') {
  const result = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    if (ignored.has(entry.name)) continue;
    const name = path.join(directory, entry.name);
    result.push(...(entry.isDirectory() ? await files(name) : [name]));
  }
  return result;
}
