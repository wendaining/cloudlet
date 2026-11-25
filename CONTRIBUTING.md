# 参与贡献

所有开发工作通过 Issue 明确目标、范围、负责人和验收条件。

开发前阅读 [技术栈](docs/technology-stack.md)，按模块采用的技术与接口约定编写代码。

本地开发使用本机 Docker 中的数据库与执行环境，环境配置见 [开发环境](docs/development.md)。

## 准备环境

安装 Go 1.27.4、Node.js 24 及以上版本、Docker Compose、Git、Make 和 ShellCheck。Windows 使用 WSL2。操作 GitHub 时安装并登录 GitHub CLI。

```sh
git clone https://github.com/wendaining/cloudlet.git
cd cloudlet
make setup
make doctor
make dev
```

make setup 安装锁定的依赖与测试浏览器，启用提交信息检查，并生成本机开发配置。账号、停止、多实例与调试操作见 [开发环境](docs/development.md)。

## 选择任务

使用问题报告、功能提案或工程任务表单创建 Issue。分诊时确认验收条件，并添加 type、area、priority 和 status 标签。负责人可以在分诊或领取时登记。

status:ready 表示可以领取并交给 Agent 实施，status:in-progress 表示负责人正在执行。阻塞条件写入 Issue，并使用 status:blocked。详细规则见 [协作流程](docs/workflow.md)。

## 编写更改

从最新 master 创建分支，名称使用 codex/编号-简述 或 feature/编号-简述。每个分支处理一个任务，涉及多个可独立验收的目标时拆分 Issue 和 PR。

每次提交只处理一个目的。

提交信息与 PR 标题建议使用 Conventional Commits：

```text
feat(gateway): 支持路径路由
fix(containers): 释放停止容器的端口
docs: 补充本地开发说明
ci: 按改动范围执行检查
```

复杂更改按能够独立说明和验证的目的提交。提交时提供完整实现和相关验证。

## 验证更改

```sh
make check
make container-check
```

make check 执行完整检查，包括真实数据库、开发环境与浏览器流程。按范围运行的命令见 [测试说明](docs/testing.md)，CI 的触发范围见 [交付与发布](docs/releases.md)。

涉及运行时、容器或交付配置时执行容器验证。测试应覆盖更改后的行为、重要错误路径和资源释放。

## 提交 PR

按照 PR 模板说明最终行为、验证结果和需要关注的限制。建议使用 Closes #编号关联任务，指定与关联 Issue 相同的负责人，并添加 type、area 与 agent:assisted 标签。

向其他协作者请求审查。合并要求 CI 通过、至少一位其他成员批准。

维护者操作见 [协作流程](docs/workflow.md)，发布操作见 [交付与发布](docs/releases.md)。
