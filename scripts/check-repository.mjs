import assert from 'node:assert/strict';
import { readdir, readFile, stat } from 'node:fs/promises';
import path from 'node:path';
import { parseDocument } from 'yaml';
import { unified } from 'unified';
import remarkParse from 'remark-parse';

const ignored = new Set(['.git', 'node_modules', '.local', 'bin', 'dist']);
async function files(directory) {
  const result = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    if (ignored.has(entry.name)) continue;
    const name = path.join(directory, entry.name);
    result.push(...(entry.isDirectory() ? await files(name) : [name]));
  }
  return result;
}
const all = await files('.');
const readJSON = async name => JSON.parse(await readFile(name, 'utf8'));
const labels = await readJSON('.github/labels.json');
assert.equal(new Set(labels.map(label => label.name)).size, labels.length, '标签名称必须唯一');
for (const label of labels) {
  assert.match(label.name, /^(type|area|priority|status|agent):[a-z0-9-]+$/u);
  assert.match(label.color, /^[0-9A-Fa-f]{6}$/u);
  assert(label.description.length > 0 && label.description.length <= 100);
}
const knownLabels = new Set(labels.map(label => label.name));
const maintainers = await readJSON('.github/maintainers.json');
assert(maintainers.triageAssignee && maintainers.defaultBranch);
for (const name of all.filter(name => /\.ya?ml$/u.test(name))) {
  const document = parseDocument(await readFile(name, 'utf8'), { uniqueKeys: true });
  assert.equal(document.errors.length, 0, `${name}: ${document.errors.join(', ')}`);
  const value = document.toJS();
  if (name.startsWith('.github/ISSUE_TEMPLATE/') && path.basename(name) !== 'config.yml') {
    assert(value.name && value.description && Array.isArray(value.body), `${name}: 缺少 Issue 表单字段`);
    for (const label of value.labels) assert(knownLabels.has(label), `${name}: 未定义标签 ${label}`);
    assert.deepEqual(value.assignees, [maintainers.triageAssignee]);
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
const parser = unified().use(remarkParse);
for (const name of all.filter(name => name.endsWith('.md'))) {
  const source = await readFile(name, 'utf8');
  const limit = { 'README.md': 90, 'AGENTS.md': 70, 'CONTRIBUTING.md': 110 }[name];
  if (limit) assert(source.trimEnd().split('\n').length <= limit, `${name}: 详细内容请移入 docs`);
  const tree = parser.parse(source);
  const definitions = new Map();
  const nodes = [];
  function visit(node) {
    nodes.push(node);
    if (node.type === 'definition') definitions.set(node.identifier, node.url);
    for (const child of node.children ?? []) visit(child);
  }
  visit(tree);
  for (const node of nodes) {
    const url = node.url ?? (node.type === 'linkReference' ? definitions.get(node.identifier) : undefined);
    if (!url || /^(?:[a-z][a-z0-9+.-]*:|#|\/)/iu.test(url)) continue;
    const destination = decodeURIComponent(url.split('#')[0]);
    if (destination) assert((await stat(path.resolve(path.dirname(name), destination))).isFile(), `${name}: 链接不存在 ${url}`);
  }
}
console.log('配置、Issue 表单、文档链接和入口文档长度检查通过');
