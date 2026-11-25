import assert from 'node:assert/strict';

export function labelNames(issue) {
  return issue.labels.map(label => typeof label === 'string' ? label : label.name);
}

export function validateTask(issue) {
  assert(!issue.pull_request && issue.state === 'open', '任务必须是开放的 Issue');

  const labels = labelNames(issue);
  assert.equal(labels.filter(label => label.startsWith('type:')).length, 1, '任务需要一个 type 标签');
  assert(labels.some(label => label.startsWith('area:')), '任务需要 area 标签');
  assert.equal(labels.filter(label => label.startsWith('priority:')).length, 1, '任务需要一个 priority 标签');

  const statuses = labels.filter(label => label.startsWith('status:'));
  assert.equal(statuses.length, 1, '任务需要一个 status 标签');
  assert(['status:ready', 'status:in-progress'].includes(statuses[0]), '任务必须处于 ready 或 in-progress 状态');
}
