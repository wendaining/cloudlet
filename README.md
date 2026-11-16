# Cloudlet

Cloudlet 是面向单机部署的 Serverless 平台，围绕 Workers、Durable Objects、Containers 和 Browser Run 提供统一的管理 API 与 HTTP 入口。

项目使用 Go 模块化单体架构，执行环境复用成熟运行时。开发通过 Issue、负责人、PR 审查和自动化验证组织。

后端选用 Gin、GORM 和 PostgreSQL，管理界面选用 React、TypeScript、Vite 和 Ant Design。详细约定见 [技术栈](docs/technology-stack.md)。

## 当前能力

| 模块 | 当前状态 |
| --- | --- |
| 管理 API | 健康检查、就绪检查和构建信息 |
| Gateway | 静态域名路由、HTTP 转发和上游错误响应 |
| CLI | 查询客户端版本、检查 API 健康状态 |
| Workers、Durable Objects、Containers、Browser Run | 已定义模块边界与开发任务 |
| 协作与交付 | Issue 表单、标签、PR 检查、Agent 交接、CI、镜像交付与版本发布 |

## 本地运行

需要 Go 1.27.4、Node.js 24 及以上版本、Git、Make 和 ShellCheck。容器验证需要 Docker。仓库管理操作需要已登录的 GitHub CLI。

```sh
make setup
make check
make run
```

在另一个终端执行：

```sh
go run ./cmd/cloudlet health
curl http://127.0.0.1:8080/api/v1/version
curl -H 'Host: demo.localhost' http://127.0.0.1:8081/api/v1/version
```

默认配置位于 [configs/cloudlet.json](configs/cloudlet.json)。管理 API 监听 8080 端口，Gateway 监听 8081 端口，地址均为本机回环地址。示例域名 demo.localhost 将请求转发至管理 API。

```sh
make container-check
```

该命令构建镜像并验证容器启动、CLI、实际转发与未知域名响应。

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
