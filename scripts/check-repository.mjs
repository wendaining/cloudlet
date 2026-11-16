import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { parseDocument } from 'yaml';
import { files } from './files.mjs';

const all = await files();
const readJSON = async name => JSON.parse(await readFile(name, 'utf8'));
for (const name of all.filter(name => name.endsWith('.json'))) await readJSON(name);
const labels = await readJSON('.github/labels.json');
assert.equal(new Set(labels.map(label => label.name)).size, labels.length, '标签名称必须唯一');
for (const label of labels) {
  assert.match(label.name, /^(type|area|priority|status|agent):[a-z0-9-]+$/u);
  assert.match(label.color, /^[0-9A-Fa-f]{6}$/u);
  assert(label.description.length > 0 && label.description.length <= 100);
}
const knownLabels = new Set(labels.map(label => label.name));
const maintainers = await readJSON('.github/maintainers.json');
assert(maintainers.defaultBranch);
for (const name of all.filter(name => /\.ya?ml$/u.test(name))) {
  const document = parseDocument(await readFile(name, 'utf8'), { uniqueKeys: true });
  assert.equal(document.errors.length, 0, `${name}: ${document.errors.join(', ')}`);
  const value = document.toJS();
  if (name.startsWith('.github/ISSUE_TEMPLATE/') && path.basename(name) !== 'config.yml') {
    assert(value.name && value.description && Array.isArray(value.body), `${name}: 缺少 Issue 表单字段`);
    for (const label of value.labels) assert(knownLabels.has(label), `${name}: 未定义标签 ${label}`);
    assert.equal(new Set(value.body.filter(field => field.id).map(field => field.id)).size, value.body.filter(field => field.id).length);
  }
  if (name.startsWith('.github/workflows/')) {
    assert(!value.on.pull_request_target, `${name}: 请使用权限受限的 PR 事件`);
    assert(value.permissions, `${name}: 需要明确声明权限`);
    for (const job of Object.values(value.jobs)) {
      if (!job.uses) assert(job['timeout-minutes'], `${name}: 任务需要超时时间`);
      for (const step of job.steps ?? []) {
        if (step.uses && !step.uses.startsWith('./')) assert.match(step.uses, /@[0-9a-f]{40}$/u, `${name}: Action 需要固定 commit`);
      }
    }
  }
}
console.log('JSON、YAML、Issue 表单和 Actions 配置检查通过');
