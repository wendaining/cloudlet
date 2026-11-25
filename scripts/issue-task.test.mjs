import assert from 'node:assert/strict';
import { test } from 'node:test';
import { validateTask } from './issue-task.mjs';

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
