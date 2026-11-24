# Cloudlet

Cloudlet 是一台服务器就能运行的 **Agentic Serverless 平台**。项目的核心目标是让 AI Agent 使用与管理控制台相同的 API，自主完成部署与验证。

平台计划统一管理 Workers、Durable Objects、Containers 和 Browser Run。Agent 可以在同一工作流程中发布 Worker、按需启动容器，再用独立浏览器访问部署地址并验证结果。

项目使用 Go 模块化单体架构，执行环境复用成熟运行时。开发通过 Issue、负责人、PR 审查和自动化验证组织。

后端选用 Gin、GORM 和 PostgreSQL，管理界面选用 React、TypeScript、Vite 和 Ant Design。完整文档从 [文档索引](docs/README.md) 进入。

## 当前能力

| 模块 | 当前状态 |
| --- | --- |
| 管理 API | 健康检查、构建信息、账号登录、持久化会话、项目与成员角色 |
| 管理界面 | React 登录、退出和环境概览，通过真实 API 访问 PostgreSQL |
| 本地开发 | 环境检查、数据库初始化、Go 与前端热更新、多实例与完整停止 |
| Gateway | 静态与数据库持久化动态路由、HTTP 转发和上游错误响应 |
| CLI | 查询客户端版本、检查 API 健康状态、列出和查看可访问项目 |
| Containers | Docker Engine 镜像摘要准备与容器生命周期适配器，按归属标签创建、启动、观测、停止和删除容器，支持回环端口与内部网络端点，并提供有期限的 HTTP 就绪探测 |
| 容量预留 | 主机与项目额度事务预留、执行标识与容器归属及容器 ID 绑定、确认释放与占用查询 |
| Workers、Durable Objects、Browser Run | 已定义模块边界与开发任务 |
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

## 许可证

采用 [Apache License 2.0](LICENSE)。
