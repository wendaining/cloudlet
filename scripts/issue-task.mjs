import assert from 'node:assert/strict';

export function labelNames(issue) {
  return issue.labels.map(label => typeof label === 'string' ? label : label.name);
}

function taskMetadataProblems(labels) {
  const problems = [];
  if (labels.filter(label => label.startsWith('type:')).length !== 1) problems.push('任务需要一个 type 标签');
  if (!labels.some(label => label.startsWith('area:'))) problems.push('任务需要 area 标签');
  if (labels.filter(label => label.startsWith('priority:')).length !== 1) problems.push('任务需要一个 priority 标签');
  return problems;
}

export function validateTask(issue) {
  assert(!issue.pull_request && issue.state === 'open', '任务必须是开放的 Issue');
  const labels = labelNames(issue);
  const [problem] = taskMetadataProblems(labels);
  assert(!problem, problem);
  const statuses = labels.filter(label => label.startsWith('status:'));
  assert.equal(statuses.length, 1, '任务需要一个 status 标签');
  assert(['status:ready', 'status:in-progress'].includes(statuses[0]), '任务必须处于 ready 或 in-progress 状态');
}

export function inspectTaskReadiness(issue, dependencies) {
  const labels = labelNames(issue);
  const statuses = labels.filter(label => label.startsWith('status:'));
  const problems = taskMetadataProblems(labels);
  if (issue.pull_request || issue.state !== 'open') problems.push('任务必须是开放的 Issue');
  if (statuses.length !== 1 || !['status:triage', 'status:blocked', 'status:ready', 'status:in-progress'].includes(statuses[0])) {
    problems.push('任务需要一个有效的 status 标签');
  }
  const remainingDependencies = dependencies.filter(
    dependency => dependency.state !== 'closed' || dependency.state_reason !== 'completed',
  );
  return { statuses, problems, remainingDependencies };
}
