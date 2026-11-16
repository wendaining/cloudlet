import assert from 'node:assert/strict';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { capture } from './development/process.mjs';

const directory = await mkdtemp(path.join(os.tmpdir(), 'cloudlet-openapi-'));
try {
  if (process.argv[2] === 'web') {
    const filename = path.join(directory, 'api.gen.ts');
    await capture('npm', ['exec', '--workspace', 'web', '--', 'openapi-typescript', '../api/openapi.yaml', '-o', filename]);
    assert.equal(await readFile('web/src/api.gen.ts', 'utf8'), await readFile(filename, 'utf8'), '请执行 make generate 并提交生成结果');
  } else {
    const filename = path.join(directory, 'api.gen.go');
    await capture('go', ['tool', 'oapi-codegen', '-generate', 'types,gin-server,client', '-package', 'apigen', '-o', filename, 'api/openapi.yaml']);
    assert.equal(await readFile('internal/apigen/api.gen.go', 'utf8'), await readFile(filename, 'utf8'), '请执行 make generate 并提交生成结果');
  }
} finally {
  await rm(directory, { recursive: true });
}
