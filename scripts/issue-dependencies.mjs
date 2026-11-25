export function labelNames(issue) {
  return issue.labels.map(label => typeof label === 'string' ? label : label.name);
}

export function isCompleted(issue) {
  return issue.state === 'closed' && issue.state_reason === 'completed';
}

export function isBlockedTask(issue) {
  const labels = labelNames(issue);
  const statuses = labels.filter(label => label.startsWith('status:'));
  return !issue.pull_request &&
    issue.state === 'open' &&
    issue.assignees.length > 0 &&
    statuses.includes('status:blocked') &&
    statuses.every(label => ['status:blocked', 'status:ready'].includes(label)) &&
    labels.filter(label => label.startsWith('type:')).length === 1 &&
    labels.some(label => label.startsWith('area:')) &&
    labels.filter(label => label.startsWith('priority:')).length === 1;
}

export function dependenciesCompleted(dependencies) {
  return dependencies.length > 0 && dependencies.every(isCompleted);
}
