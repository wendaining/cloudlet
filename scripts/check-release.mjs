import assert from 'node:assert/strict';
import semver from 'semver';

const tag = process.argv[2];
assert(tag?.startsWith('v') && semver.valid(tag.slice(1)), '发布标签必须使用 v 开头的 SemVer，例如 v0.1.0');
console.log(`发布标签 ${tag} 检查通过`);
