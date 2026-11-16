# 技术栈

本文明确各模块采用的技术与使用边界。技术选型作为实现依据，依赖随对应模块接入。当前已实现的能力见 [README](../README.md)。

本地开发使用每位成员电脑上的 PostgreSQL 容器，Go 与 Vite 在本机运行。服务器通过 Compose 部署相同技术的服务与数据库。连接地址、账户与数据卷的约定见 [本地开发与服务器部署](development.md)。

## 后端与数据

| 部分 | 技术 | 职责 |
| --- | --- | --- |
| 核心服务 | Go | 管理 API、资源生命周期、运行时编排与 CLI |
| 管理 HTTP API | Gin | 路由、参数校验、认证与权限中间件、统一响应 |
| Gateway | net/http、httputil.ReverseProxy | HTTP 转发、连接管理与请求路由 |
| 元数据库 | PostgreSQL | 用户、资源、部署版本、路由配置与生命周期状态 |
| 数据访问 | GORM、gorm.io/driver/postgres | 元数据模型、查询、关联和事务 |
| 数据库迁移 | goose、SQL 迁移文件 | 按版本管理表结构、索引和数据变更 |
| 日志 | log/slog | 结构化 JSON 日志 |

Gin 提供管理 API 所需的路由分组、参数校验和中间件。管理请求、业务逻辑、数据库访问分别放在各自模块中，业务方法接收 context.Context。HTTP 参数类型与数据库模型分别定义。

GORM 管理 PostgreSQL 中的平台元数据。查询传递请求上下文，多表变更通过事务完成。数据库变更写入 migrations 目录中的 SQL 文件，由 goose 显式执行。服务启动时验证连接与所需的数据库版本，初始化失败时终止启动。

## 管理界面

| 部分 | 技术 | 职责 |
| --- | --- | --- |
| 页面框架 | React、TypeScript | 管理页面与组件，启用 TypeScript strict |
| 开发与构建 | Vite | 开发服务器、热更新与静态产物构建 |
| 页面路由 | React Router Declarative Mode | 页面切换、资源详情路径与 URL 参数 |
| UI 组件 | Ant Design | 表格、表单、布局、对话框和状态反馈 |
| 样式 | Ant Design 主题配置、CSS Modules | 统一主题与组件局部样式 |
| 服务端数据 | TanStack Query | 查询、缓存、变更请求与刷新 |
| API 客户端 | openapi-typescript、openapi-fetch | 生成接口类型，执行具有类型约束的 HTTP 请求 |
| 代码检查 | TypeScript、ESLint、Prettier | 类型、代码规则和格式检查 |

管理界面采用浏览器端 SPA，构建产物为静态文件。React Router 管理页面路由，TanStack Query 管理服务端数据，组件状态管理本地交互。表单使用 Ant Design Form。

请求统一经过 API 客户端。查询与变更请求关闭自动重试，错误交给调用方处理并展示。开发时由 Vite 代理管理 API，部署时由管理服务提供静态文件与 API，页面路由使用 index.html 入口。

## 接口契约

管理 API 使用 REST 与 JSON，接口定义采用 OpenAPI 3.1，存放在 api/openapi.yaml。资源请求、响应、错误结构与认证要求在契约中声明。

Go 使用 oapi-codegen 生成接口类型、Gin 服务端接口与客户端代码。pkg/client 提供业务调用方法，CLI 和 MCP 通过该客户端访问管理 API。前端通过 openapi-typescript 生成类型，并由 openapi-fetch 发起请求。

契约变化与调用方更改在同一项任务中验证。生成文件随实现提交，CI 执行生成一致性检查。

## 执行环境与接入

| 模块 | 技术 | 职责 |
| --- | --- | --- |
| Workers | workerd | 执行部署后的 JavaScript Worker |
| Worker 构建 | esbuild、TypeScript | 打包为 JavaScript ES Module，使用 tsc 检查 TypeScript 类型 |
| Durable Objects | workerd Durable Objects、SQLite | 对象绑定、运行时状态与持久化 |
| Containers | Docker Engine、官方 Go SDK | 镜像、容器生命周期与资源配额 |
| Browser Run | Node.js、TypeScript、Playwright、Chromium | 在受管理容器中执行浏览器会话 |
| CLI | Go、Cobra | 命令、参数与共享 API 客户端 |
| Agent 接口 | 官方 MCP Go SDK | 将管理 API 操作暴露为 MCP 工具 |
| 单机部署 | Docker、Docker Compose | 部署管理服务、PostgreSQL 与所需运行环境 |

Durable Objects 的 SQLite 存储由 workerd 管理。Browser Run 复用容器模块创建、限制和回收资源，Go 管理服务通过内部接口控制浏览器会话。Playwright 与浏览器镜像使用对应版本。

## 工程与验证

Go 版本由 go.mod 指定。前端、浏览器模块与仓库工具使用 Node.js 24 LTS 和 npm。Go 依赖由 go.mod、go.sum 锁定，JavaScript 依赖由各工程的 package.json、package-lock.json 锁定。

根目录 package.json 管理协作与文档工具。前端工程放在 web 目录，浏览器运行环境放在 runtimes/browser 目录，各自管理依赖与构建配置。新增工程同步声明对应的 CI 检查范围。

| 验证范围 | 技术与要求 |
| --- | --- |
| Go 行为与 HTTP 接口 | Go testing、httptest，使用真实 HTTP 服务并启用 race detector |
| 数据库集成 | Testcontainers for Go，运行真实 PostgreSQL 并执行迁移 |
| 前端逻辑 | Vitest，验证纯函数与状态处理 |
| 页面与用户流程 | Playwright Test，使用真实浏览器、管理 API 与数据库 |
| 前端构建 | TypeScript、ESLint、Prettier、Vite build |
| 运行环境 | 使用实际 workerd、Docker 与 Chromium 验证部署和资源回收 |

依赖精确版本在接入时写入对应依赖文件，容器镜像锁定 digest。库的调用方式以所锁定版本的官方文档为依据。

## 官方资料

| 范围 | 资料 |
| --- | --- |
| 管理 API 与数据库 | [Gin](https://gin-gonic.com/en/docs/)、[GORM](https://gorm.io/docs/)、[goose](https://pressly.github.io/goose/) |
| 页面与交互 | [React](https://react.dev/learn/build-a-react-app-from-scratch)、[Ant Design 与 Vite](https://ant.design/docs/react/use-with-vite/)、[React Router](https://reactrouter.com/start/modes)、[TanStack Query](https://tanstack.com/query/latest/docs/framework/react/overview) |
| 接口生成 | [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen)、[openapi-typescript](https://openapi-ts.dev/introduction)、[openapi-fetch](https://openapi-ts.dev/openapi-fetch/) |
| 执行与测试 | [workerd](https://github.com/cloudflare/workerd)、[Playwright 容器](https://playwright.dev/docs/docker)、[Testcontainers for Go](https://github.com/testcontainers/testcontainers-go)、[MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk) |
