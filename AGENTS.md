# Agent 工作约定

Cloudlet 是 Go 模块化单体项目。任务范围与验收条件以关联 Issue 为依据。

## 开始工作

阅读 [CONTRIBUTING.md](CONTRIBUTING.md)、[技术栈](docs/technology-stack.md)、[架构说明](docs/architecture.md) 和 [Agent 工作流程](docs/agents.md)。检查当前文件与工作区状态，保留用户的现有更改。

任务开始前确认 Issue 已分配一位负责人，具备 type、area、priority 标签，处于 ready 或 in-progress 状态。Agent 任务还需要 agent:ready 标签。

## 实施要求

每个分支关联一个明确任务，每次提交只处理一个目的。提交信息与 PR 标题遵循 Conventional Commits。

复用成熟运行时与文件格式解析库。发现配置、依赖或运行错误时明确返回错误。测试使用真实服务、真实文件和实际依赖。

通过共享管理 API 实现 CLI 与 Agent 能力。模块依赖与接口变化同步更新文档。中文说明使用完整词语，代码标识符保持英文。

## 验证与提交

按 [改动范围](docs/releases.md) 执行对应检查与相关集成测试，make check 用于完整验证。涉及容器或交付配置时执行 make container-check。记录实际命令、结果和运行限制。

PR 使用 Closes #编号关联 Issue，指定同一位负责人，添加 type、area 和 agent:assisted 标签。由其他成员审查并合并。

详细操作与边界见 [docs/agents.md](docs/agents.md)。
