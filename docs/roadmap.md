# 开发任务

每个模块通过独立 Issue 确认实现范围、负责人和验收结果。实现遵循 [技术栈](technology-stack.md)，这里定义核心能力的验收目标。

资源和部署的共同规则见 [平台设计](platform-design.md)，执行生命周期与状态归属见 [执行环境设计](runtime-design.md)。下表列出目标实现，当前完成情况见 [README](../README.md)。

| 模块 | 实现内容 | 验收目标 |
| --- | --- | --- |
| [管理 API](https://github.com/wendaining/cloudlet/issues/6) | 项目成员、资源身份、Deployment、Execution、Artifact、Operation 与配额 | 创建和查询真实资源，验证授权、幂等、容量预留与中断恢复 |
| 平台产物与部署 | 内容摘要、本地产物、绑定校验、Secret 版本与活动版本事务 | 产物完整后建立引用，失败发布保留原活动版本，引用有效时保留文件 |
| [Containers](https://github.com/wendaining/cloudlet/issues/2) | Docker Engine API、稳定资源标识、独立执行、数据卷与资源准入 | 实际 HTTP 容器经 Gateway 访问，后端重启接管，停止及删除确认资源释放 |
| [Workers](https://github.com/wendaining/cloudlet/issues/3) | 固定 workerd、项目执行单元、不可变模块包、绑定与部署激活 | 部署真实 Worker，验证更新、就绪、运行时故障与不支持的配置 |
| [Worker 弹性扩缩容](#worker-弹性扩缩容) | 根据 HTTP 并发负载自动调整单机内无状态 Worker 的执行副本，按就绪状态分流 | 使用真实 workerd 和 HTTP 压力测试验证扩容、缩容、配额与流量连续性 |
| [Durable Objects](https://github.com/wendaining/cloudlet/issues/4) | 稳定命名空间、原生 SQLite、唯一执行所有者与迁移记录 | 跨代码更新保持对象身份，验证事务、并发、独占交接与重启后的数据 |
| [Browser Run](https://github.com/wendaining/cloudlet/issues/5) | 独立会话容器、Playwright、Chromium sandbox、期限与产物导出 | 真实截图、后端重启重连、会话隔离、超时及完整停止后的资源释放 |
| Gateway | 路由快照、版本引用、流式转发、WebSocket 与资源生命周期 | 活动版本和路由保持一致，验证请求排空、交接响应和故障后的恢复 |
| CLI 与 Agent 接口 | 共享客户端、资源权限、Operation 查询与 MCP | 同一 API 支持调用和状态查询，权限及错误具有一致语义 |
| 管理界面 | 项目、资源、部署、Operation 与真实运行状态 | 通过真实 API 操作，展示活动版本、可用性、失败原因与能力限制 |

## 实现依赖与验收

资源管理任务提供共同模型、操作记录与准入接口。产物与部署任务提供不可变内容和活动版本事务。执行模块在各自任务中接入真实运行时与状态观测，Gateway 同步接入已提交路由。

Browser Run 使用 Containers 的执行能力。DO 的命名空间和所有权设计随 Workers 接入，DO 行为在原生运行时中验收。跨模块通过资源标识和已定义接口协作。

验收覆盖同一 DO 对象跨 Worker 版本更新与 Go 重启保持数据，原容器继续运行，未过期浏览器会话重新连接，以及部分创建、删除和交接中断后的恢复。详细条件见 [业务验收](testing.md)。

每个实现任务应同时包含相关的错误路径、清理行为、集成测试和使用说明。

## Worker 弹性扩缩容

弹性扩缩容是 Worker 托管执行的核心目标。在基础 Worker 部署、Gateway 运行时路由和资源准入可用后，平台根据 HTTP 并发负载自动增减无状态 Worker 的执行副本。首期在单台主机上运行，至少保留一个副本，副本容量受主机和项目配额约束。

Gateway 将请求分发给同一活动版本的就绪副本。缩容时，先停止向待回收副本发送新请求，在排空期限内等待已有请求完成，再停止副本。[命名空间独占存储规则](runtime-design.md#do-命名空间与存储)适用于绑定 Durable Objects 的 Worker。

使用真实 workerd 和经过 Gateway 的 HTTP 请求验收。持续加压时，平台自动启动副本并向多个就绪副本分流，请求得到同一活动版本的预期响应。负载持续下降时，平台排空并回收多余副本。达到主机或项目容量上限时，副本数保持在预算内。
