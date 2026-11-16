# 架构

Cloudlet 在一台 Linux 主机上管理计算资源。管理 API 和 Gateway 属于同一 Go 工程，分别监听独立端口。外部运行时由对应模块管理。

管理 API 采用 Gin，元数据通过 GORM 访问 PostgreSQL，管理界面采用 React 与 TypeScript。各项技术的职责、接口契约和工程约定见 [技术栈](technology-stack.md)。

## 模块边界

| 路径 | 职责 |
| --- | --- |
| cmd/server | 配置加载、信号处理和服务启动 |
| cmd/cloudlet | 基于共享客户端的命令行入口 |
| internal/config | 严格读取 JSON 配置并验证字段 |
| internal/controlplane | 管理 HTTP 接口 |
| internal/gateway | 按域名选择上游并转发 HTTP 请求 |
| internal/server | 监听端口与服务生命周期 |
| internal/version | 构建版本和提交标识 |
| internal/workers | workerd 进程、代码版本和部署配置 |
| internal/durableobjects | 对象命名空间、绑定和持久化配置 |
| internal/containers | Docker Engine 资源与容器生命周期 |
| internal/browser | 复用容器管理的 Playwright 会话 |
| pkg/client | CLI 与其他客户端共享的 API 客户端 |

执行模块当前提供包边界，开发目标见 [开发任务](roadmap.md)。管理元数据使用 PostgreSQL 的接入工作属于资源管理任务，Durable Objects 的存储能力随 workerd 集成验证。

## 当前接口

| 地址 | 方法 | 行为 |
| --- | --- | --- |
| /healthz | GET | 返回进程健康状态 |
| /readyz | GET | 返回服务就绪状态 |
| /api/v1/version | GET | 返回 version 与 commit |

两组监听端口完成初始化后才开始接受请求。当前就绪状态代表 API 与 Gateway 已启动。增加数据库或运行时依赖时，必须同时扩展就绪检查。

Gateway 使用配置中的 host 匹配请求域名，匹配时忽略大小写、末尾句点和请求端口。target 支持 HTTP 与 HTTPS。请求路径、查询参数和响应经 Go ReverseProxy 转发。未知域名返回 404，上游连接失败返回 502。

管理接口缺少用户认证，默认绑定本机回环地址。配置路由由主机维护者管理。公网管理访问与用户代码执行需要完成相应的认证和隔离任务。

## 配置与生命周期

配置只在启动时读取，未知字段、重复域名、无效地址或目标协议会阻止启动。使用 -config 指定配置文件。

API 与 Gateway 使用 HTTP 头读取超时和空闲连接超时。收到 SIGTERM 或 SIGINT 后停止接收连接，在十秒内等待已有请求完成。进程输出 JSON 日志。

## 执行环境

Workers 集成 workerd。Durable Objects 随同运行时验证持久化与并发语义。容器管理使用 Docker Engine API。Browser Run 复用容器生命周期，并通过 Playwright 实现浏览器操作。

接入执行环境时需要确定资源配额、进程隔离、网络权限、启动与退出行为。验收必须使用实际运行时。
