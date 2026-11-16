# 测试与验收

所有 HTTP、数据库、容器与页面流程使用实际依赖。运行完整检查前执行 make setup，并确认本机 Docker 可以访问。

| 命令 | 检查范围 |
| --- | --- |
| npm run lint:docs | Markdown、链接与入口文档长度 |
| npm run lint:config | JSON、YAML、Issue 表单与 Actions 约定 |
| npm run lint:tooling | 脚本语法、ShellCheck、配置文件与真实端口检查 |
| make test | Go race 测试、真实 HTTP、Gateway 转发与服务停止 |
| make test-integration | Testcontainers PostgreSQL、迁移、账号、持久化会话与就绪检查 |
| make web-check | TypeScript、ESLint、Prettier、Vite 构建与接口生成一致性 |
| make dev-check | 真实本机进程、数据库、热重载、完整停止、多实例与失败清理 |
| make test-e2e | 真实 Chromium 在桌面和手机尺寸下完成登录、刷新和退出 |
| make generate-check | Go 接口生成结果与契约的一致性 |
| make container-check | Go 镜像构建、实际启动、CLI、路由与正常退出 |
| make check | 执行格式、静态检查、测试、构建、依赖审计与上述本地集成检查 |

make generate 从 api/openapi.yaml 同时生成 Go 类型、Gin 接口、客户端和前端类型，生成文件随实现提交。Go 和前端的检查分别比较临时生成结果。

## 开发流程验证

开发流程测试创建独立配置与随机可用端口，通过与成员相同的开发入口运行 Go、Vite 和 PostgreSQL。测试验证同名控制器拒绝重复启动、错误的 reset 确认不会停止服务、真实 API 登录和 Gateway 转发。

热重载检查在测试期间创建独立 Go 源文件，验证实际后端进程更换、会话保留，以及受管理 PostgreSQL 执行容器保持原始启动时间。随后验证真实编译错误会停止旧后端，修复源码后服务恢复。测试完成后删除该源文件。

完整停止验证端口释放、执行容器停止、数据库数据保留和再次启动后的登录会话。多实例检查验证一个实例停止后另一个实例继续提供服务。迁移版本不兼容时验证启动失败与资源清理。

页面测试使用独立环境，验证错误密码、登录成功、刷新后的会话、退出和页面宽度。截图与失败记录位于 .local/playwright，环境日志与配置位于 .local/checks 和各测试实例目录。

测试结束后清理所属容器、网络与数据卷。镜像缓存继续复用。文档检查独立于代码、数据库和浏览器流程。
