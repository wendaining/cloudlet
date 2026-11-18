import assert from 'node:assert/strict';

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
const issueLabels = issue.labels.nodes.map(label => label.name);
const issueAssignees = issue.assignees.nodes.map(assignee => assignee.login).sort();
const statusLabels = labelsWithPrefix(issueLabels, 'status:');

assert.equal(issue.state, 'OPEN', '关联任务必须处于打开状态');
assert.equal(labelsWithPrefix(issueLabels, 'type:').length, 1, '关联任务必须有一个 type 标签');
assert(labelsWithPrefix(issueLabels, 'area:').length > 0, '关联任务至少需要一个 area 标签');
assert.equal(labelsWithPrefix(issueLabels, 'priority:').length, 1, '关联任务必须有一个 priority 标签');
assert.equal(statusLabels.length, 1, '关联任务必须有一个 status 标签');
assert(
  ['status:ready', 'status:in-progress'].includes(statusLabels[0]),
  '关联任务必须处于 ready 或 in-progress 状态',
);
assert.deepEqual(pullRequestAssignees, issueAssignees, 'PR 与关联任务的负责人必须一致');

if (pullRequestLabels.includes('agent:assisted')) {
  assert(issueLabels.includes('agent:ready'), 'Agent 参与的 PR 要求关联任务具有 agent:ready 标签');
}

console.log(`PR #${pullRequestNumber} 策略检查通过`);
