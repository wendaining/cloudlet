#!/usr/bin/env node
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { createRequire } from 'node:module';
import { mkdir, readFile, stat } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';

import { build } from 'esbuild';

const root = path.dirname(fileURLToPath(import.meta.url));

function parseArguments(argv) {
  const { values: options } = parseArgs({
    args: argv,
    options: {
      entry: { type: 'string', default: path.join(root, 'src/index.ts') },
      outfile: { type: 'string', default: path.join(root, 'dist/index.mjs') },
      tsconfig: { type: 'string', default: path.join(root, 'tsconfig.json') },
    },
    strict: true,
    allowPositionals: false,
  });
  for (const [name, value] of Object.entries(options)) {
    if (value === '') {
      throw new Error(`option --${name} requires a value`);
    }
  }
  return options;
}

async function requireFile(file, description) {
  let info;
  try {
    info = await stat(file);
  } catch (error) {
    throw new Error(`${description} ${file} is not readable: ${error.message}`, { cause: error });
  }
  if (!info.isFile()) {
    throw new Error(`${description} ${file} must be a regular file`);
  }
}

function runTypeScript(tsconfig, flags = []) {
  const require = createRequire(import.meta.url);
  const tsc = path.join(path.dirname(require.resolve('typescript/package.json')), 'bin/tsc');
  const result = spawnSync(process.execPath, [tsc, '--noEmit', '--pretty', 'false', '--project', tsconfig, ...flags], {
    cwd: root,
    encoding: 'utf8',
  });
  if (result.error) {
    throw new Error(`run TypeScript compiler: ${result.error.message}`, { cause: result.error });
  }
  if (result.status !== 0) {
    process.stderr.write(result.stdout ?? '');
    process.stderr.write(result.stderr ?? '');
    throw new Error(`TypeScript check failed for ${tsconfig}`);
  }
  return result.stdout;
}

function typeCheck(tsconfig, entry) {
  const files = runTypeScript(tsconfig, ['--listFilesOnly']).split(/\r?\n/);
  if (!files.some(file => path.resolve(root, file) === entry)) {
    throw new Error(`entry ${entry} is not part of TypeScript configuration ${tsconfig}`);
  }
  runTypeScript(tsconfig);
}

async function main() {
  const options = parseArguments(process.argv.slice(2));
  const entry = path.resolve(options.entry);
  const outfile = path.resolve(options.outfile);
  const tsconfig = path.resolve(options.tsconfig);

  await requireFile(entry, 'entry');
  await requireFile(tsconfig, 'TypeScript configuration');

  typeCheck(tsconfig, entry);
  await mkdir(path.dirname(outfile), { recursive: true });
  try {
    await build({
      absWorkingDir: root,
      entryPoints: [entry],
      outfile,
      bundle: true,
      format: 'esm',
      platform: 'neutral',
      target: 'es2022',
      charset: 'utf8',
      legalComments: 'none',
      sourcemap: false,
      minify: false,
      treeShaking: true,
      external: ['cloudflare:*', 'node:*'],
      logLevel: 'silent',
      metafile: false,
    });
  } catch (error) {
    for (const diagnostic of error.errors ?? []) {
      const location = diagnostic.location
        ? `${diagnostic.location.file}:${diagnostic.location.line}:${diagnostic.location.column}`
        : '';
      console.error(`${location}${location ? ': ' : ''}${diagnostic.text}`);
    }
    throw new Error(`bundle ${path.relative(root, entry)}`);
  }

  const output = await readFile(outfile);
  const digest = createHash('sha256').update(output).digest('hex');
  console.log(
    JSON.stringify({
      entry: path.relative(root, entry),
      outfile: path.relative(root, outfile),
      bytes: output.length,
      sha256: digest,
    }),
  );
}

main().catch(error => {
  console.error(error.message);
  process.exitCode = 1;
});
