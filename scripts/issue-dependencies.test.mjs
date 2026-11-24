import assert from 'node:assert/strict';
import { test } from 'node:test';
import { dependenciesCompleted, isBlockedTask } from './issue-dependencies.mjs';

const labels = ['status:blocked', 'type:feature', 'area:browser', 'priority:p1'];
const task = { state: 'open', assignees: [{ login: 'owner' }], labels };

test('checks assigned blocked issues with task labels and permits retrying a partial transition', () => {
  assert.equal(isBlockedTask(task), true);
  assert.equal(isBlockedTask({ ...task, labels: [...labels, 'status:ready'].map(name => ({ name })) }), true);
});

test('skips closed, unassigned, incomplete and independently changed tasks', () => {
  const skipped = [
    { ...task, state: 'closed' },
    { ...task, pull_request: {} },
    { ...task, assignees: [] },
    { ...task, labels: labels.filter(label => !label.startsWith('area:')) },
    { ...task, labels: [...labels, 'priority:p2'] },
    { ...task, labels: [...labels, 'status:in-progress'] },
    { ...task, labels: labels.filter(label => label !== 'status:blocked') },
  ];
  for (const issue of skipped) assert.equal(isBlockedTask(issue), false);
});

test('requires registered dependencies that were all closed as completed', () => {
  const completed = { state: 'closed', state_reason: 'completed' };
  assert.equal(dependenciesCompleted([completed, completed]), true);
  assert.equal(dependenciesCompleted([]), false);
  assert.equal(dependenciesCompleted([completed, { state: 'open' }]), false);
  assert.equal(dependenciesCompleted([completed, { state: 'closed', state_reason: 'not_planned' }]), false);
  assert.equal(dependenciesCompleted([{ state: 'closed', state_reason: null }]), false);
});
