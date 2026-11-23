import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdtemp, readFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const packageRoot = path.dirname(fileURLToPath(import.meta.url));
const buildScript = path.join(packageRoot, 'build.mjs');

function runBuild(args) {
  return spawnSync(process.execPath, [buildScript, ...args], {
    cwd: packageRoot,
    encoding: 'utf8',
  });
}

function sha256(content) {
  return createHash('sha256').update(content).digest('hex');
}

test('build produces the same single-file module on repeated runs', async () => {
  const outputDir = await mkdtemp(path.join(os.tmpdir(), 'cloudlet-worker-build-'));
  const first = path.join(outputDir, 'first.mjs');
  const second = path.join(outputDir, 'second.mjs');

  const firstRun = runBuild(['--outfile', first]);
  assert.equal(firstRun.status, 0, firstRun.stderr);
  const secondRun = runBuild(['--outfile', second]);
  assert.equal(secondRun.status, 0, secondRun.stderr);

  const firstOutput = await readFile(first);
  const secondOutput = await readFile(second);
  assert.equal(sha256(firstOutput), sha256(secondOutput));
  assert.match(firstOutput.toString('utf8'), /as default/);
  assert.doesNotMatch(firstOutput.toString('utf8'), /sourceMappingURL=/);
});

test('build reports TypeScript diagnostics before bundling', async () => {
  const outputDir = await mkdtemp(path.join(os.tmpdir(), 'cloudlet-worker-type-error-'));
  const result = runBuild([
    '--entry',
    'testdata/type-error/index.ts',
    '--tsconfig',
    'testdata/type-error/tsconfig.json',
    '--outfile',
    path.join(outputDir, 'index.mjs'),
  ]);

  assert.equal(result.status, 1);
  assert.match(result.stderr, /TS2322/);
  assert.match(result.stderr, /Type 'number' is not assignable to type 'string'/);
});

test('build rejects unknown options and missing entries', () => {
  const unknown = runBuild(['--unknown', 'value']);
  assert.equal(unknown.status, 1);
  assert.match(unknown.stderr, /unknown option --unknown/);

  const missing = runBuild(['--entry', 'missing.ts']);
  assert.equal(missing.status, 1);
  assert.match(missing.stderr, /entry .*missing\.ts is not readable/);
});
