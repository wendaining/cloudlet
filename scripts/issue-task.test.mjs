import assert from 'node:assert/strict';
import { test } from 'node:test';
import { inspectTaskReadiness, planIssueStatusUpdate, validateTask } from './issue-task.mjs';

const taskLabels = ['type:task', 'area:automation', 'area:cli', 'priority:p1'];
const readyLabels = [...taskLabels, 'status:ready'];

test('accepts a ready issue using task metadata alone', () => {
  assert.doesNotThrow(() => validateTask({ state: 'open', labels: readyLabels.map(name => ({ name })) }));
});

test('accepts a claimed issue in progress', () => {
  assert.doesNotThrow(() => validateTask({ state: 'open', labels: [...taskLabels, 'status:in-progress'] }));
});

test('rejects closed issues and pull requests', () => {
  assert.throws(() => validateTask({ state: 'closed', labels: readyLabels }), /开放的 Issue/u);
  assert.throws(() => validateTask({ state: 'open', pull_request: {}, labels: readyLabels }), /开放的 Issue/u);
});

test('rejects missing, conflicting and non-actionable statuses', () => {
  const invalidLabels = [
    taskLabels,
    [...readyLabels, 'status:blocked'],
    [...taskLabels, 'status:triage'],
    [...taskLabels, 'status:blocked'],
  ];
  for (const labels of invalidLabels) {
    assert.throws(() => validateTask({ state: 'open', labels }), /status 标签|ready 或 in-progress/u);
  }
});

test('rejects issues without the required classification', () => {
  assert.throws(() => validateTask({ state: 'open', labels: readyLabels.slice(1) }), /type 标签/u);
  assert.throws(() => validateTask({ state: 'open', labels: [readyLabels[0], ...readyLabels.slice(3)] }), /area 标签/u);
  assert.throws(() => validateTask({ state: 'open', labels: [...readyLabels, 'priority:p2'] }), /priority 标签/u);
});

test('reports open, cancelled and unknown dependencies while retaining completed edges', () => {
  const issue = { state: 'open', labels: [...taskLabels, 'status:blocked'] };
  const dependencies = [
    { state: 'closed', state_reason: 'completed', number: 1 },
    { state: 'open', state_reason: null, number: 2 },
    { state: 'closed', state_reason: 'not_planned', number: 3 },
    { state: 'closed', state_reason: null, number: 4 },
  ];
  assert.deepEqual(inspectTaskReadiness(issue, dependencies), {
    statuses: ['status:blocked'],
    problems: [],
    remainingDependencies: dependencies.slice(1),
  });
  assert.equal(dependencies.length, 4);
});

test('checks migrated triage issues and reports all metadata and status problems', () => {
  const issue = { state: 'open', labels: [...taskLabels, 'status:triage'] };
  assert.deepEqual(inspectTaskReadiness(issue, []), {
    statuses: ['status:triage'],
    problems: [],
    remainingDependencies: [],
  });
  const result = inspectTaskReadiness({ state: 'closed', labels: ['status:triage', 'status:ready'] }, []);
  assert.equal(result.problems.length, 5);
});

test('adds an assignee before changing status and can resume after each write', () => {
  const issue = { state: 'open', labels: [...readyLabels, 'legacy-label'], assignees: [] };
  const claim = current => planIssueStatusUpdate(current, 'status:ready', 'status:in-progress', 'owner');
  const assigned = { ...issue, assignees: [{ login: 'Owner' }] };
  const labels = [...assigned.labels, 'status:in-progress'];
  assert.equal(claim(issue), 'assign');
  assert.equal(claim(assigned), 'add');
  assert.equal(claim({ ...assigned, labels }), 'remove');
  assert.equal(claim({ ...assigned, labels: labels.filter(label => label !== 'status:ready') }), null);
  assert.throws(() => claim({ ...issue, assignees: [{ login: 'other' }] }), /已有其他负责人/u);
  assert.throws(() => claim({ ...issue, labels: taskLabels }), /状态与预期不符/u);
});

test('resumes only the expected status transition and leaves other labels and assignees alone', () => {
  const issue = { state: 'open', labels: [...taskLabels, 'status:blocked', 'legacy-label'], assignees: [{ login: 'owner' }] };
  const advance = current => planIssueStatusUpdate(current, 'status:blocked', 'status:ready');
  const labels = [...issue.labels, 'status:ready'];
  assert.equal(advance(issue), 'add');
  assert.equal(advance({ ...issue, labels }), 'remove');
  assert.equal(advance({ ...issue, labels: labels.filter(label => label !== 'status:blocked') }), null);
  assert.throws(
    () => advance({ ...issue, labels: [...labels, 'status:triage'] }),
    /状态与预期不符/u,
  );
});
