import assert from 'node:assert/strict';
import { validateTask } from './issue-task.mjs';

const token = process.env.GITHUB_TOKEN;
const repository = process.env.GITHUB_REPOSITORY;
const pullRequestNumber = Number(process.env.PULL_REQUEST_NUMBER);

assert(token, '缺少 GITHUB_TOKEN');
assert(repository, '缺少 GITHUB_REPOSITORY');
assert(Number.isInteger(pullRequestNumber) && pullRequestNumber > 0, '缺少有效的 PR 编号');

const [owner, name, extra] = repository.split('/');
assert(owner && name && !extra, 'GITHUB_REPOSITORY 必须使用 owner/name 格式');

const response = await fetch('https://api.github.com/graphql', {
  method: 'POST',
  headers: {
    Accept: 'application/vnd.github+json',
    Authorization: `Bearer ${token}`,
    'Content-Type': 'application/json',
    'User-Agent': 'cloudlet-pr-policy',
  },
  body: JSON.stringify({
    query: `
      query PullRequestPolicy($owner: String!, $name: String!, $number: Int!) {
        repository(owner: $owner, name: $name) {
          pullRequest(number: $number) {
            title
            baseRefName
            assignees(first: 20) {
              nodes { login }
            }
            labels(first: 50) {
              nodes { name }
            }
            closingIssuesReferences(first: 10) {
              nodes {
                number
                state
                assignees(first: 20) {
                  nodes { login }
                }
                labels(first: 50) {
                  nodes { name }
                }
              }
            }
          }
        }
      }
    `,
    variables: {
      owner,
      name,
      number: pullRequestNumber,
    },
  }),
});

if (!response.ok) {
  throw new Error(`读取 PR 失败: HTTP ${response.status} ${await response.text()}`);
}

const payload = await response.json();
if (payload.errors?.length) {
  throw new Error(`读取 PR 失败: ${payload.errors.map(error => error.message).join('; ')}`);
}

const pullRequest = payload.data?.repository?.pullRequest;
assert(pullRequest, `PR #${pullRequestNumber} 不存在`);

const pullRequestLabels = pullRequest.labels.nodes.map(label => label.name);
const linkedIssues = pullRequest.closingIssuesReferences.nodes;
const pullRequestAssignees = pullRequest.assignees.nodes.map(assignee => assignee.login).sort();

assert.equal(pullRequest.baseRefName, 'master', 'PR 必须指向 master');
assert.match(
  pullRequest.title,
  /^(build|chore|ci|docs|feat|fix|perf|refactor|revert|style|test)(\([a-z0-9./-]+\))?: .+$/u,
  'PR 标题必须使用 Conventional Commits',
);
assert.equal(linkedIssues.length, 1, 'PR 必须关联一个会关闭的任务 Issue');

function labelsWithPrefix(labels, prefix) {
  return labels.filter(label => label.startsWith(prefix));
}

assert.equal(labelsWithPrefix(pullRequestLabels, 'type:').length, 1, 'PR 必须有一个 type 标签');
assert(labelsWithPrefix(pullRequestLabels, 'area:').length > 0, 'PR 至少需要一个 area 标签');

const issue = linkedIssues[0];
const issueAssignees = issue.assignees.nodes.map(assignee => assignee.login).sort();

validateTask({ state: issue.state.toLowerCase(), labels: issue.labels.nodes });
assert.deepEqual(pullRequestAssignees, issueAssignees, 'PR 与关联任务的负责人必须一致');

console.log(`PR #${pullRequestNumber} 策略检查通过`);
