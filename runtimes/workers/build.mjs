#!/usr/bin/env node
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { createRequire } from 'node:module';
import { mkdir, readFile, stat } from 'node:fs/promises';
import path from 'node:path';

import { build } from 'esbuild';

const defaults = {
  entry: 'src/index.ts',
  outfile: 'dist/index.mjs',
  tsconfig: 'tsconfig.json',
};

function parseArguments(argv) {
  const options = { ...defaults };
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (!argument.startsWith('--')) {
      throw new Error(`unexpected argument ${argument}`);
    }
    const [name, inlineValue] = argument.slice(2).split('=', 2);
    if (!(name in options)) {
      throw new Error(`unknown option --${name}`);
    }
    const value = inlineValue ?? argv[++index];
    if (value === undefined || value === '') {
      throw new Error(`option --${name} requires a value`);
    }
    options[name] = value;
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

function typeCheck(tsconfig) {
  const require = createRequire(import.meta.url);
  const tsc = path.join(path.dirname(require.resolve('typescript/package.json')), 'bin/tsc');
  const result = spawnSync(process.execPath, [tsc, '--noEmit', '--pretty', 'false', '--project', tsconfig], {
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
}

async function main() {
  const options = parseArguments(process.argv.slice(2));
  const root = process.cwd();
  const entry = path.resolve(root, options.entry);
  const outfile = path.resolve(root, options.outfile);
  const tsconfig = path.resolve(root, options.tsconfig);

  await requireFile(entry, 'entry');
  await requireFile(tsconfig, 'TypeScript configuration');

  typeCheck(tsconfig);
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
