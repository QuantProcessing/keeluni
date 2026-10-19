# trader

trader 是一个薄的、无状态的手动交易应用：单人登录、会话、把浏览器请求转发到 gateway 与 hedger、提供前端静态文件。它**没有数据库**，也从不直接访问交易所。

## 路由

| 路径 | 作用 |
|---|---|
| `POST /api/auth/login` | 密码登录，设置会话 cookie |
| `POST /api/auth/logout` | 退出 |
| `GET /api/auth/session` | 当前是否已登录 |
| `GET /api/apps` | 部署了哪些应用（`{hedge: bool}`），前端据此显示「对冲」入口 |
| `/api/gw/v1/*` | 透传到 gateway `/v1/*` |
| `GET /api/ws` | 透传到 gateway `/v1/ws` |
| `/api/hedge/v1/*` | 透传到 hedger `/v1/*`（设置 `HEDGER_URL` 时） |
| `GET /api/hedge/ws` | 透传到 hedger `/v1/ws` |
| `/api/*`（其他） | 404 |
| `/*` | 前端静态文件；未知路径回退到 `index.html`（前端路由） |

## 转发

- 校验会话、`Origin` 同源与 `X-KeelUni: 1`（非 GET）后，把浏览器的 cookie 与 `X-KeelUni` 头去掉，换成服务间 bearer token 再转发。
- WebSocket 握手同样校验 cookie 与 `Origin`。
- 上游不可达时返回 `GATEWAY_UNAVAILABLE` / `HEDGER_UNAVAILABLE`（502）。

## 会话与密码

| 项 | 做法 |
|---|---|
| 密码 | 首次启动生成随机密码并打印一次；`$TRADER_DATA_DIR/password` 保存 argon2id 哈希；`trader passwd` 从标准输入读取新密码（至少 12 个字符） |
| 会话 | 32 字节随机 token，只在内存中；cookie `keeluni_session`；空闲 12 小时、绝对 7 天过期；重启即需重新登录 |
| 暴力破解 | 按 IP 指数退避（1 秒起，上限 15 分钟），并有全局失败计数；被限时返回 `THROTTLED`（429） |

## 错误码

| code | HTTP | 含义 |
|---|---|---|
| `UNAUTHENTICATED` | 401 | 需要登录 |
| `CSRF` | 403 | 跨域请求被拒（Origin 不同源或缺少 `X-KeelUni`） |
| `THROTTLED` | 429 | 登录尝试过多 |
| `GATEWAY_UNAVAILABLE` / `HEDGER_UNAVAILABLE` | 502 | 上游不可达 |
| `NOT_FOUND` | 404 | 未知接口 |
