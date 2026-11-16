# 协作流程

默认分支为 master。任务记录、代码更改、检查结果与人工审查共同构成开发记录。

## Issue

创建 Issue 时填写问题报告、功能提案或工程任务表单。每个任务包含目标、实现范围和可以检查的验收条件。存在前置依赖时，写明关联编号。

新任务由 wendaining 负责分诊。分诊完成时指定实际实施者，并确认类型、模块和优先级。每个任务保留一位负责人，协作成员通过关联任务或 PR 参与。

| 标签 | 使用规则 |
| --- | --- |
| type:feature、type:bug、type:task、type:docs | 每个任务选择一个类型 |
| area:api、area:gateway、area:workers、area:durable-objects、area:containers、area:browser、area:cli、area:web、area:automation | 至少选择一个相关模块 |
| priority:p0、priority:p1、priority:p2 | 选择一个优先级，新任务默认为 p2 |
| status:triage | 正在确认范围、验收条件与负责人 |
| status:ready | 已具备实施条件 |
| status:in-progress | 正在实施 |
| status:blocked | 已记录阻塞条件 |
| agent:ready | 维护者已确认可交给 Agent 执行 |
| agent:assisted | PR 包含 Agent 参与的更改 |

同类 type、priority、status 标签互斥。添加新标签时，Issue coordination 移除同类旧标签。GitHub 的关闭状态表示任务完成，关闭后移除 status 与 agent:ready 标签。

ready、in-progress 和 agent:ready 要求开放 Issue、唯一负责人、一个 type、至少一个 area 和一个 priority。检查失败会使对应 Actions 任务失败，维护者需要修正 Issue 元数据。

## 分支与小增量

分支使用 codex/编号-简述 或 feature/编号-简述。每个 PR 对应一个验收目标，可以将相关的更改拆成数次提交。能够单独发布或审查的目标分别建立任务与 PR。

每次提交只处理一个目的。

提交信息与 PR 标题通过 commitlint 检查。采用 Conventional Commits，header 最多 100 个字符。合并使用 squash，主分支保留经过检查的 PR 标题。

## PR

PR 模板要求提供关联任务、最终更改、实际验证结果、风险与限制。有 Agent 参与时填写 Agent 记录。

PR policy 从 GitHub 读取全部提交和关联 Issue，验证标题、提交信息、PR 标签、唯一负责人及任务状态。关闭任务的关键字使用 Closes #编号，PR 指向 master。关联 Issue 负责人与 PR 负责人必须相同。

Issue 的标签或负责人改变后，需要重新运行关联 PR 的 PR policy。修改 PR 的标签、负责人、标题、描述或代码都会自动重新执行检查。

代码审查者检查行为、边界、测试证据和文档。至少一位其他协作者批准，最后一次推送也需要其他成员批准。新增提交使原批准失效。所有审查讨论必须处理完成。

CODEOWNERS 将默认审查责任交给 wendaining。由 wendaining 提交的更改向其他协作者请求审查。仓库规则接受其他有权限成员的批准。

## 自动化

| Workflow | 触发与职责 |
| --- | --- |
| CI | master 更新与 PR 变化时，按改动范围执行检查 |
| PR policy | PR 元数据或代码变化时检查协作要求 |
| Issue coordination | 新建、重新打开、标记、分配和关闭任务时维护标签与分诊 |
| Agent handoff | 维护者手动选择 Issue，生成 Agent 任务资料 |
| Delivery | 由 Release 调用，发布版本对应的容器镜像 |
| Release | 版本标签触发完整验证，发布镜像、二进制文件和校验和 |

PR 检查使用只读权限。Issue 自动化只修改 Issue 元数据。镜像与版本发布任务分别获得所需的 packages 与 contents 写入权限。Actions 固定为完整 commit，Dependabot 每周提出依赖更新。

依赖更新 PR 遵循相同审查要求。分诊负责人为更新建立工程任务，填写验收条件，再将 PR 关联到任务。

## 维护仓库设置

维护者修改 .github/labels.json 或 .github/maintainers.json 后执行：

```sh
make repo-sync
```

该命令使用当前 GitHub 身份同步标签、squash 设置和默认分支保护，需要仓库管理权限。现有标签会更新，配置之外的标签保留。

默认分支禁止删除与强制推送，合并需要 CI、PR policy 和一次其他成员的审查。规则适用于仓库管理员。
