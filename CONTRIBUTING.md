# 参与贡献

所有开发工作通过 Issue 明确目标、范围、负责人和验收条件。

## 准备环境

安装 Go 1.27.4、Node.js 24 及以上版本、Git 和 Make。使用容器时安装 Docker，操作 GitHub 时安装并登录 GitHub CLI。

```sh
git clone https://github.com/wendaining/cloudlet.git
cd cloudlet
make setup
make check
```

make setup 安装锁定的依赖，并为当前仓库启用提交信息检查。

## 选择任务

使用问题报告、功能提案或工程任务表单创建 Issue。维护者确认验收条件，指定一位负责人，并添加 type、area、priority 和 status 标签。

status:ready 表示可以开始实施，status:in-progress 表示负责人正在执行。阻塞条件写入 Issue，并使用 status:blocked。详细规则见 [协作流程](docs/workflow.md)。

## 编写更改

从最新 master 创建分支，名称使用 codex/编号-简述 或 feature/编号-简述。每个分支处理一个任务，涉及多个可独立验收的目标时拆分 Issue 和 PR。

每次提交只处理一个目的。单次提交的手写内容更改最多 400 行，每个 PR 最多 600 行，计算新增与删除的总数。package-lock.json 与 go.sum 属于生成文件，CI 仍检查依赖和锁定结果。

提交信息与 PR 标题使用 Conventional Commits：

```text
feat(gateway): 支持路径路由
fix(containers): 释放停止容器的端口
docs: 补充本地开发说明
ci: 检查 PR 关联任务
```

复杂更改按能够独立说明和验证的目的提交。提交时提供完整实现和相关验证。

## 验证更改

```sh
make check
make container-check
```

make check 覆盖格式、模块依赖、静态检查、真实 HTTP 集成测试、构建、文档、配置、Actions 和依赖漏洞检查。

涉及运行时、容器或交付配置时执行容器验证。测试应覆盖更改后的行为、重要错误路径和资源释放。

## 提交 PR

按照 PR 模板说明最终行为、验证结果和需要关注的限制。正文使用 Closes #编号，指定与关联 Issue 相同的负责人，并添加 type 与 area 标签。Agent 参与的更改添加 agent:assisted，并填写 Agent 记录。

PR policy 检查每条提交、PR 标题、提交规模、标签、负责人及关联任务。依赖更新 PR 同样需要维护者建立或关联相应任务。

向其他协作者请求审查。合并要求 CI 与 PR policy 通过，至少一位其他成员批准，审查讨论已处理。使用 squash 合并，PR 标题成为主分支上的提交标题。

维护者操作见 [协作流程](docs/workflow.md)，发布操作见 [交付与发布](docs/releases.md)。
