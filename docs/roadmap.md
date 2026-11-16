# 开发任务

每个模块通过独立 Issue 确认实现范围、负责人和验收结果。实现遵循 [技术栈](technology-stack.md)，这里定义核心能力的验收目标。

| 模块 | 实现内容 | 验收目标 |
| --- | --- | --- |
| [Containers](https://github.com/wendaining/cloudlet/issues/2) | Docker Engine API、资源标识、启动停止与销毁、资源配额 | 从实际镜像启动 HTTP 服务，经 Gateway 访问，销毁后释放资源 |
| [Workers](https://github.com/wendaining/cloudlet/issues/3) | workerd 进程、JS 部署、版本信息、路由登记 | 部署真实 Worker，通过 HTTP 调用，并验证进程退出与配置失败 |
| [Durable Objects](https://github.com/wendaining/cloudlet/issues/4) | 对象命名空间、Worker 绑定、SQLite 持久化 | 同一对象持久化计数，验证并发语义、进程重启和对象隔离 |
| [Browser Run](https://github.com/wendaining/cloudlet/issues/5) | 复用容器管理、Chromium 与 Playwright、会话超时 | 创建会话，访问页面并截图，销毁与超时均释放容器 |
| [管理 API](https://github.com/wendaining/cloudlet/issues/6) | 资源模型、PostgreSQL 元数据、认证、生命周期状态 | 创建与查询真实资源，重启后恢复管理信息，拒绝无权限操作 |
| Gateway | 动态路由登记、请求日志与生命周期联动 | 部署产生可访问路由，销毁后撤销路由，错误请求返回明确状态 |
| CLI 与 Agent 接口 | 共享客户端、资源操作与 MCP | 同一管理 API 支持命令行和 Agent 调用，错误具有一致语义 |
| 管理界面 | React、TypeScript、Vite 与 Ant Design | 通过真实管理 API 完成资源操作，展示加载、运行与错误状态 |

Browser Run 依赖 Containers。Durable Objects 依赖 Workers。资源创建流程需要同步明确元数据与路由的变更方式。

每个实现任务应同时包含相关的错误路径、清理行为、集成测试和使用说明。
