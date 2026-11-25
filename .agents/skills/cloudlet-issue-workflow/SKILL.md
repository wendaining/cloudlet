---
name: cloudlet-issue-workflow
description: 通过 Cloudlet 的 GitHub Issue 依赖选择并领取当前可开发的任务，在关联 PR 合并后维护依赖并解除下游阻塞。
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
4. 领取前重新读取选定 Issue 和依赖。将 `status:ready` 换成 `status:in-progress`，未分配时在同一次 `gh issue edit` 中登记获授权的实施者。读取结果，确认负责人和唯一的状态标签后开始开发。发现已有人领取或有开放 PR 时，检查下一项。用户要求继续已有任务时，核对当前负责人、`status:in-progress` 和关联 PR 后继续。

## 复查并更新依赖与状态

- 核对 GitHub 依赖与 Issue 记录。任务正文、关联 PR 或合并后的代码明确证明有遗漏的必需前置时，用 `gh issue edit --add-blocked-by` 补登记。添加前检查是否会形成依赖环。只有证据明确表明前置已不再需要时，才用 `--remove-blocked-by` 移除。已完成且仍有效的关系保留，通过前置 Issue 的关闭状态解除阻塞。
- 对仅有 `status:blocked` 这一个状态标签的开放 Issue，前置全部以 `completed` 关闭、其他明确记录的阻塞均已解除、任务元数据齐全时，将状态换成 `status:ready`。没有登记前置时，须从任务记录核实具体阻塞原因和解除证据。发现带 `status:ready` 的任务仍有明确的未完成前置或人工阻塞时，将状态换成 `status:blocked`。
- 证据不足或依赖冲突时，保留状态并记录具体原因，继续检查其他候选。只有需要改变任务范围或作出产品决定时才单独请求人工判断。已在实施的任务发现阻塞时，在交接中说明原因和受影响的任务。
- 修改前重读 Issue 和依赖。用同一次 `gh issue edit` 添加新状态并移除旧状态，随后读取结果确认关系和状态。API 失败时报告原始错误及相关 Issue 编号。

## 完成交付并开启下游

- PR 使用 `Closes #编号`，并由其他协作者审查。提交 PR 或通过 CI 后，当前 Issue 仍处于实施中。
- 用 `gh pr view` 确认关联 PR 状态为 `MERGED`，并确认 Issue 已按 `completed` 关闭后，读取该 Issue 的 `blocking`，复查本仓库中每个仍开放的直接下游，更新有充分证据的依赖和状态。交接时列出已就绪的 Issue、仍阻塞的 Issue 及其剩余前置。获授权继续开发时，按领取规则选择下一项。
- 会话在合并前结束时，交接中记录 PR、关联 Issue 和当前已知的直接下游。后续会话收到合并结果时，从该 Issue 继续复查。即使没有原会话的交接记录，下一次领取前对开放阻塞任务的复查也会处理已完成的前置。
