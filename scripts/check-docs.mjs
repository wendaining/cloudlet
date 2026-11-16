import assert from 'node:assert/strict';
import { readFile, stat } from 'node:fs/promises';
import path from 'node:path';
import { unified } from 'unified';
import remarkParse from 'remark-parse';
import { files } from './files.mjs';

const parser = unified().use(remarkParse);
for (const name of (await files()).filter(name => name.endsWith('.md'))) {
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
console.log('文档链接和入口文档长度检查通过');
