import assert from 'node:assert/strict';

const taskStatuses = ['status:triage', 'status:blocked', 'status:ready', 'status:in-progress'];

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
  if (statuses.length !== 1 || !taskStatuses.includes(statuses[0])) {
    problems.push('任务需要一个有效的 status 标签');
  }
  const remainingDependencies = dependencies.filter(
    dependency => dependency.state !== 'closed' || dependency.state_reason !== 'completed',
  );
  return { statuses, problems, remainingDependencies };
}

export function planIssueStatusUpdate(issue, from, to, assignee) {
  assert(!issue.pull_request && issue.state === 'open', '任务必须是开放的 Issue');
  assert(taskStatuses.includes(from) && taskStatuses.includes(to) && from !== to, '需要两个不同的有效状态');
  assert(to !== 'status:in-progress' || assignee, '领取任务需要指定获授权的负责人');
  assert(!assignee || to === 'status:in-progress', '只有转入实施中时可以指定负责人');

  const labels = labelNames(issue);
  const statuses = labels.filter(label => label.startsWith('status:'));
  assert(statuses.length > 0 && statuses.every(status => status === from || status === to), '当前状态与预期不符，请人工核对');

  const assignees = issue.assignees.map(person => person.login.toLowerCase());
  if (assignee) {
    assert(assignees.length === 0 || assignees.includes(assignee.toLowerCase()), '任务已有其他负责人');
  }

  const nextLabels = [...labels.filter(label => label !== from && label !== to), to];
  if (['status:ready', 'status:in-progress'].includes(to)) validateTask({ ...issue, labels: nextLabels });
  if (assignee && assignees.length === 0) return 'assign';
  if (!statuses.includes(to)) return 'add';
  if (statuses.includes(from)) return 'remove';
  return null;
}
