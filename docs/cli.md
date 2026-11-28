# CLI 操作查询

CLI 使用管理 API 查询项目内的操作：

```sh
cloudlet operation get --project PROJECT_ID --id OPERATION_ID
```

命令使用 `CLOUDLET_COOKIE` 环境变量中的已认证 Cookie 请求 API。也可以通过 `--cookie` 显式传入 Cookie 请求头。请将 Cookie 作为凭据保管。

默认输出为表格。使用 `--output json` 输出包含操作 ID、动作、阶段、结果和时间戳的 JSON。可以通过 `--endpoint` 指定管理 API 地址。

等待终态时显式指定正数时限：

```sh
cloudlet operation wait --project PROJECT_ID --id OPERATION_ID --timeout 30s --output json
```

等待命令立即查询，操作仍在进行时每隔一秒再次查询。时限覆盖 API 请求和轮询间隔。等待期间按 Ctrl+C 可取消。成功或失败时输出一次终态结果，超时或取消时通过标准错误说明原因。

退出码是稳定的脚本接口：

| 退出码 | 含义 |
| --- | --- |
| 0 | 操作成功 |
| 1 | 参数、网络、API 或响应错误 |
| 2 | 操作失败 |
| 3 | 单次查询发现操作仍在进行 |
| 4 | 未认证（HTTP 401） |
| 5 | 操作不存在或当前账号无法访问该项目 |
| 124 | 等待时间已到 |
| 130 | 等待已取消 |

查询使用项目范围的操作列表，并要求 API 会话对该项目有访问权限。
