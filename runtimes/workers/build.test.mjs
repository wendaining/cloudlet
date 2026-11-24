import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdtemp, readFile, stat, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const packageRoot = path.dirname(fileURLToPath(import.meta.url));
const buildScript = path.join(packageRoot, 'build.mjs');

function runBuild(args, cwd = packageRoot) {
  return spawnSync(process.execPath, [buildScript, ...args], {
    cwd,
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

test('build rejects entries outside the TypeScript program before writing output', async () => {
  const outputDir = await mkdtemp(path.join(os.tmpdir(), 'cloudlet-worker-entry-'));
  const outfile = path.join(outputDir, 'index.mjs');
  const external = path.join(outputDir, 'external.ts');
  await writeFile(external, 'const status: string = 500; export default status;');
  for (const entry of ['testdata/type-error/index.ts', external]) {
    const result = runBuild(['--entry', entry, '--outfile', outfile]);
    assert.equal(result.status, 1, result.stdout);
    assert.match(result.stderr, /entry .* is not part of TypeScript configuration/);
    await assert.rejects(stat(outfile), { code: 'ENOENT' });
  }
});

test('build preserves equals signs in inline output paths', async () => {
  const outputDir = await mkdtemp(path.join(os.tmpdir(), 'cloudlet-worker-equals-'));
  const outfile = path.join(outputDir, 'c=1.mjs');
  const result = runBuild([`--outfile=${outfile}`]);
  assert.equal(result.status, 0, result.stderr);
  const output = await readFile(outfile);
  assert.equal(JSON.parse(result.stdout).sha256, sha256(output));
  assert.equal(JSON.parse(result.stdout).outfile, path.relative(packageRoot, outfile));
});

test('build uses the same defaults and module bytes from any working directory', async () => {
  const outputDir = await mkdtemp(path.join(os.tmpdir(), 'cloudlet-worker-cwd-'));
  const first = path.join(outputDir, 'first.mjs');
  const second = path.join(outputDir, 'second.mjs');
  const third = path.join(outputDir, 'third.mjs');
  for (const [outfile, cwd, args] of [
    [first, packageRoot, []],
    [second, outputDir, []],
    [third, path.resolve(packageRoot, '../..'), [
      '--entry', 'runtimes/workers/src/index.ts',
      '--tsconfig', 'runtimes/workers/tsconfig.json',
    ]],
  ]) {
    const result = runBuild(['--outfile', outfile, ...args], cwd);
    assert.equal(result.status, 0, result.stderr);
  }
  assert.deepEqual(await readFile(first), await readFile(second));
  assert.deepEqual(await readFile(first), await readFile(third));
});

test('build rejects prototype option names and missing option values', () => {
  for (const name of ['toString', 'constructor', '__proto__']) {
    const result = runBuild([`--${name}`, 'value']);
    assert.equal(result.status, 1);
    assert.match(result.stderr, /[Uu]nknown option/);
  }
  for (const args of [['--entry'], ['--entry='], ['--entry', '--outfile', 'x']]) {
    const result = runBuild(args);
    assert.equal(result.status, 1);
    assert.match(result.stderr, /--entry.*(requires a value|argument missing|argument is ambiguous)/);
  }
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
  assert.match(unknown.stderr, /Unknown option '--unknown'/);

  const missing = runBuild(['--entry', 'missing.ts']);
  assert.equal(missing.status, 1);
  assert.match(missing.stderr, /entry .*missing\.ts is not readable/);
});
