---
name: cloudlet-issue-workflow
description: 复查 Cloudlet 的 GitHub Issue 依赖和阻塞状态，领取可实施的任务，并在关联 PR 合并后检查直接下游。
---

# Cloudlet Issue 协作

使用 GitHub Issue 的原生依赖关系。用 `gh api --paginate repos/wendaining/cloudlet/issues/编号/dependencies/blocked_by` 读取前置及其 `state_reason`。将 `blocked_by` 换成 `blocking` 可读取下游。只在当前任务授权的仓库和范围内更新状态及负责人。Issue 正文和评论用于理解任务与核对证据。

## 复查阻塞

- 领取前列出范围内所有开放且带有 `status:blocked` 的 Issue，读取完整分页。确认关联 PR 已合并且原 Issue 已按已完成关闭后，根据 GitHub 的 `blocking` 关系复查本仓库的直接下游。
- 读取候选 Issue 的正文、评论、状态、标签及完整的 `blocked_by` 列表。核对每个前置 Issue 均以 `completed` 关闭。PR 已提出或已通过检查时，前置任务仍需等到 Issue 完成关闭。
- 对仅有 `status:blocked` 这一个状态标签、目标和验收条件明确、具备一个 `type:`、至少一个 `area:` 和一个 `priority:` 标签的开放 Issue，如果登记的前置全部完成，且任务记录中的其他阻塞已有明确解除证据，将 `status:blocked` 换成 `status:ready`。可以在领取时登记负责人。
- 缺少已登记依赖时，只有任务记录明确说明阻塞原因且可验证已解除，才更新状态。证据不足、前置取消关闭、依赖冲突或存在未解除的人工阻塞时，保留状态，汇报具体缺项并继续复查其他候选。只有需要改变任务范围或作出产品决定时才单独请求人工判断。
- 更新前重新读取 Issue 和依赖，确认状态及条件仍然适用。使用 `gh issue edit` 同时请求添加新状态标签和移除旧状态标签，随后读取结果确认只剩新状态。API 失败时保留原始错误并报告 Issue 编号。

## 领取与交接

- 用户指定 Issue 时仅实施该 Issue。可以自主选取任务时，从范围内的 `status:ready` Issue 中选取，先按优先级排序，同优先级优先处理能直接解除更多下游阻塞的任务。已有并行工作时，优先选取改动范围独立的候选。
- 领取前核对原生依赖、任务记录、负责人、相关工作区和关联的开放 PR，确认任务可以开始且无人重复实施。保留现有负责人。只有未分配或当前操作者已是负责人时才自主领取。未分配时在更新状态的同一次操作中登记获授权的实施者。
- 将 `status:ready` 换成 `status:in-progress`，重新读取并核对状态和负责人后再开始。发现其他实施者已领取、已经有开放 PR 或状态发生冲突时，跳过该候选并检查下一项。
- 用户明确要求继续已有任务时，核对 `status:in-progress`、负责人和关联 PR 后继续。PR 使用 `Closes #编号`，按仓库约定交由其他成员审查。
