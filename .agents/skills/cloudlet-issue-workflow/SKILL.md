---
name: cloudlet-issue-workflow
description: 通过 Cloudlet 的 GitHub Issue 依赖选择并领取当前可开发的任务，在关联 PR 合并后复查依赖并解除下游阻塞。
---

# Cloudlet Issue 协作

GitHub Issue 的 `blocked_by` 和 `blocking` 记录任务图的关系，Issue 的开闭、关闭原因和标签记录任务状态。只在当前任务授权的范围内修改这些记录。Issue 正文和评论用于理解任务与核对证据。

## 找到当前可做的任务

1. 用户指定 Issue 时，仅检查该任务是否可开始。用户授权自行选取时，先复查开放且带有 `status:blocked` 的 Issue，再读取开放且带有 `status:ready` 的 Issue，按授权的模块和实施者筛选。用下列接口读取完整分页，排除返回结果中的 PR。

   ```sh
   gh api --paginate 'repos/wendaining/cloudlet/issues?state=open&labels=status%3Ablocked&per_page=100'
   gh api --paginate 'repos/wendaining/cloudlet/issues?state=open&labels=status%3Aready&per_page=100'
   ```

2. 读取候选 Issue 的正文、评论、标签、负责人和完整依赖。将下列命令中的 `40` 换成候选编号。前置 Issue 必须以 `completed` 关闭。开放和取消关闭的前置都按未完成处理。任务范围和验收条件必须明确，标签须包含一个 `type:`、至少一个 `area:` 和一个 `priority:`。

   ```sh
   gh api --paginate repos/wendaining/cloudlet/issues/40/dependencies/blocked_by
   gh api --paginate repos/wendaining/cloudlet/issues/40/dependencies/blocking
   ```

3. 只自主领取未分配或已由当前实施者负责的任务。检查关联的开放 PR 和相关工作区，排除正在实施的任务。先按优先级排序。同优先级时，优先选择完成后能让更多直接下游具备实施条件的任务。计算时检查每个下游的其他前置是否已完成。已有并行工作时，优先选改动范围独立的候选。
4. 领取前重新读取选定 Issue 和依赖。将 `status:ready` 换成 `status:in-progress`，未分配时在同一次 `gh issue edit` 中登记获授权的实施者。读取结果，确认负责人和唯一的状态标签后开始开发。发现已有人领取或有开放 PR 时，检查下一项。写入失败或结果不明时，先重读状态和负责人再决定是否重试。同一任务只交给一个会话领取，共用 GitHub 账号的并行会话也须提前划分任务。用户要求继续已有任务时，核对当前负责人、`status:in-progress`、关联 PR 和交接记录后继续。

## 复查并更新依赖与状态

- 日常复查读取 GitHub 依赖和任务记录。发现漏登或不再适用的依赖时，记录 Issue、依赖方向和证据，交由维护者确认。已完成且仍有效的关系保留，通过前置 Issue 的关闭状态解除阻塞。
- 对仅有 `status:blocked` 这一个状态标签的开放 Issue，前置全部以 `completed` 关闭、其他明确记录的阻塞均已解除、任务元数据齐全时，将状态换成 `status:ready`。没有登记前置时，须从任务记录核实具体阻塞原因和解除证据。发现带 `status:ready` 的任务仍有明确的未完成前置或人工阻塞时，将状态换成 `status:blocked`。
- 证据不足或依赖冲突时，保留状态并记录具体原因，继续检查其他候选。只有需要改变任务范围或作出产品决定时才单独请求人工判断。领取前重读候选任务的前置，已完成的前置重新打开或改为取消关闭时重新判断任务状态。已在实施的任务出现新阻塞时，暂停依赖该前置的工作，通知负责人并在交接中记录证据，由负责人决定是否转为阻塞。
- 修改前重读 Issue 和依赖。用同一次 `gh issue edit` 添加新状态并移除旧状态，随后读取结果确认状态。API 失败时报告原始错误及相关 Issue 编号。

## 完成交付并开启下游

- PR 使用 `Closes #编号`，并由其他协作者审查。提交 PR 或通过 CI 后，当前 Issue 仍处于实施中。
- 用 `gh pr view` 确认关联 PR 状态为 `MERGED`，并确认 Issue 已按 `completed` 关闭后，读取该 Issue 的 `blocking`，复查本仓库中每个仍开放的直接下游，依据已有依赖和交付证据更新状态。交接时列出已就绪的 Issue、仍阻塞的 Issue 及其剩余前置。获授权继续开发时，按领取规则选择下一项。
- 会话在合并前结束时，交接中记录 PR、关联 Issue 和当前已知的直接下游。后续会话收到合并结果时，从该 Issue 继续复查。即使没有原会话的交接记录，下一次领取前对开放阻塞任务的复查也会处理已完成的前置。

## 维护者授权的依赖变更

维护者须明确授权本次增删的 Issue、前置和方向。核对授权者身份并保留授权依据。核对任务记录、两端 Issue 和当前依赖。添加前沿拟新增前置的 `blocked_by` 递归检查是否能到达当前 Issue，读取完整分页。证据不足、有环或无法核对完整路径时，向维护者报告并保留现有关系。

下例使用 #176 和 #175 展示操作方向。用已获授权的实际编号替换示例，并从读取结果确认前置的数值 `id`。添加时确认关系尚未存在，移除时确认关系仍存在。只运行对应已授权操作的命令。

```sh
gh api repos/wendaining/cloudlet/issues/175 --jq .id
```

添加前置时，将 `BLOCKING_ISSUE_ID` 设为上一步核对过的数值 ID。

```sh
gh api --method POST repos/wendaining/cloudlet/issues/176/dependencies/blocked_by -F "issue_id=${BLOCKING_ISSUE_ID:?请先设置已核对的前置 ID}"
```

移除前置时使用同一个已核对的 ID。

```sh
gh api --method DELETE "repos/wendaining/cloudlet/issues/176/dependencies/blocked_by/${BLOCKING_ISSUE_ID:?请先设置已核对的前置 ID}"
```

写入后重读完整的 `blocked_by` 确认结果。结果不明时先读取再决定是否重试。将授权依据、操作者、关系变更理由和验证结果记录在对应任务或交接中。依赖变更完成后，按正常规则另行判断任务状态。接口参数见 [GitHub Issue 依赖 API](https://docs.github.com/en/rest/issues/issue-dependencies?apiVersion=2022-11-28)。
