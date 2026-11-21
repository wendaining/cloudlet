# Agent 工作约定

Cloudlet 是 Go 模块化单体项目。任务范围与验收条件以关联 Issue 为依据。

## 开始工作

阅读 [CONTRIBUTING.md](CONTRIBUTING.md)、[技术栈](docs/technology-stack.md)、[架构说明](docs/architecture.md) 和 [Agent 工作流程](docs/agents.md)。检查当前文件与工作区状态，保留用户的现有更改。

任务开始前确认 Issue 具备 type、area、priority 标签，处于 ready 或 in-progress 状态。Agent 任务还需要 agent:ready 标签。

## 实施要求

每个分支关联一个明确任务，每次提交只处理一个目的。提交信息与 PR 标题遵循 Conventional Commits。

复用成熟运行时与文件格式解析库。

遵循快速失败（fail fast）。在入口验证输入、配置和必需依赖。发现错误时立即终止当前操作，保留原始错误并补充失败步骤及相关资源信息。必需配置或依赖不可用时终止启动。禁止吞掉错误、静默使用备用值或在失败后继续执行依赖该结果的步骤。能够提前验证的条件应在修改状态前检查。

通过共享管理 API 实现 CLI 与 Agent 能力。模块依赖与接口变化同步更新文档。中文说明使用完整词语，代码标识符保持英文。

## 验证与提交

单元测试是代码交付的一部分。新增或修改业务逻辑时，编写或更新单元测试，覆盖正常行为、边界条件和关键失败路径。修复缺陷时，先添加能复现缺陷的回归测试，再验证修复后通过。优先测试可观察行为，验证错误返回和失败后的状态。

纯逻辑使用独立单元测试。涉及文件、数据库、HTTP、容器或浏览器的行为，使用真实文件、真实服务和实际依赖进行相关集成测试。禁止使用 mock、伪造响应或为通过测试而改变生产行为。

按 [改动范围](docs/releases.md) 执行对应检查与相关集成测试，make check 用于完整验证。涉及容器或交付配置时执行 make container-check。记录实际命令、结果和运行限制。

建议在 PR 中使用 Closes #编号关联 Issue，指定负责人，添加 type、area 和 agent:assisted 标签。由其他成员审查并合并。

详细操作与边界见 [docs/agents.md](docs/agents.md)。
