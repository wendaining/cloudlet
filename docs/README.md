# 文档索引

项目设计、开发操作与课程交付物均从本文进入。根 README 只保留项目入口与本地运行方式。

## 项目设计

| 文档 | 内容 |
| --- | --- |
| [架构概览](architecture.md) | 当前能力、模块边界、接口与运行边界 |
| [平台资源与部署设计](platform-design.md) | 资源身份、项目权限、不可变部署与恢复规则 |
| [执行环境设计](runtime-design.md) | Workers、Durable Objects、Containers 与 Browser Run |
| [技术栈](technology-stack.md) | 后端、前端、数据库、接口与测试工具 |
| [开发任务](roadmap.md) | 核心模块的实现范围与验收目标 |

## 开发与交付

| 文档 | 内容 |
| --- | --- |
| [开发环境](development.md) | 本机依赖、数据库连接与服务器部署 |
| [Worker 发布](workers.md) | 首发配置、共享管理 API、版本查询与实际验收 |
| [容器生命周期与公开访问](containers.md) | 容器配置、创建、停止、再次启动、删除、就绪路由与失败清理 |
| [CLI 操作查询](cli.md) | Operation 查询命令与稳定退出码 |
| [实施清单](development-deployment-checklist.md) | 本地开发与部署的命令、任务和验收条件 |
| [测试与验收](testing.md) | 检查范围、真实依赖与业务验收 |
| [交付与发布](releases.md) | CI、镜像、版本发布及使用方式 |

## 协作流程

| 文档 | 内容 |
| --- | --- |
| [协作流程](workflow.md) | Issue、标签、负责人、提交与 PR |
| [Agent 工作流程](agents.md) | 任务交接、执行证据与人工审查 |

## 课程交付物

| 文档 | 内容 |
| --- | --- |
| [第 1 课需求调研与选题](course/lesson-01-requirements-research.md) | 项目定位、调研计划、现有依据与待验证内容 |
| [第 2 课用户故事与验收标准](course/lesson-02-user-stories.md) | 三类角色、交付批次、24 条用户故事与 Given、When、Then 验收标准 |
| [第 3 课需求规约](course/lesson-03-srs.md) | 功能、非功能需求、用例图和核心场景 |
| [第 3 课需求追溯矩阵 v0](course/lesson-03-traceability.md) | 需求 ID、用例、Issue 和调研依据 |
| [第 3 课课堂讲解提纲](course/lesson-03-presentation.md) | 8 分钟讲解安排和评审问题 |
