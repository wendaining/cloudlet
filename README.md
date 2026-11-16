# Cloudlet

Cloudlet 是面向单机部署的 Serverless 平台，围绕 Workers、Durable Objects、Containers 和 Browser Run 提供统一的管理 API 与 HTTP 入口。

项目使用 Go 模块化单体架构，执行环境复用成熟运行时。开发通过 Issue、负责人、PR 审查和自动化验证组织。

后端选用 Gin、GORM 和 PostgreSQL，管理界面选用 React、TypeScript、Vite 和 Ant Design。详细约定见 [技术栈](docs/technology-stack.md)。

## 当前能力

| 模块 | 当前状态 |
| --- | --- |
| 管理 API | 健康检查、构建信息、账号登录和持久化会话 |
| 管理界面 | React 登录、退出和环境概览，通过真实 API 访问 PostgreSQL |
| 本地开发 | 环境检查、数据库初始化、Go 与前端热更新、多实例与完整停止 |
| Gateway | 静态域名路由、HTTP 转发和上游错误响应 |
| CLI | 查询客户端版本、检查 API 健康状态 |
| Workers、Durable Objects、Containers、Browser Run | 已定义模块边界与开发任务 |
| 协作与交付 | Issue 表单、标签、PR 检查、Agent 交接、CI、镜像交付与版本发布 |

## 本地运行

需要 Go 1.27.4、Node.js 24 及以上版本、Docker Compose、Git、Make 和 ShellCheck。Windows 成员在 WSL2 中开发。

```sh
make setup
make doctor
make dev
```

打开 <http://127.0.0.1:5173>。开发账号与密码由 make setup 生成，保存在本机 .env.local。Go、Vite 在本机运行，PostgreSQL 使用本机容器。

退出时按 Ctrl+C，或在另一个终端执行：

```sh
make dev-stop
```

停止操作保留数据库与持久化数据。管理 API 默认监听 8080，Gateway 默认监听 8081。多实例、调试、迁移与数据重置见 [开发环境](docs/development.md)。

```sh
make check
```

完整检查使用真实 PostgreSQL、HTTP 和 Chromium。容器构建检查使用 make container-check，具体范围见 [测试说明](docs/testing.md)。

## 参与开发

从 [CONTRIBUTING.md](CONTRIBUTING.md) 开始。Agent 执行任务前阅读 [AGENTS.md](AGENTS.md)。

| 文档 | 内容 |
| --- | --- |
| [技术栈](docs/technology-stack.md) | 后端、前端、数据库、接口与测试工具 |
| [开发环境](docs/development.md) | 本机依赖、数据库连接与服务器部署 |
| [实施清单](docs/development-deployment-checklist.md) | 本地开发与部署的命令、任务和验收条件 |
| [架构](docs/architecture.md) | 模块职责、接口与运行边界 |
| [协作流程](docs/workflow.md) | Issue、标签、负责人、提交与 PR |
| [Agent 工作流程](docs/agents.md) | 任务交接、执行证据与人工审查 |
| [开发任务](docs/roadmap.md) | 核心模块的验收目标 |
| [交付与发布](docs/releases.md) | CI、镜像、版本发布及使用方式 |

## 许可证

采用 [Apache License 2.0](LICENSE)。
