# 部署与运维

## 运行形态

```
浏览器 ──HTTPS/cookie──> trader（单进程）
                          ├─ 静态文件（web/dist）
                          ├─ /api/auth/*        登录 / 登出 / 会话
                          ├─ /api/gw/*  ──bearer──> gateway /v1/*   （透传）
                          ├─ /api/ws    ──bearer──> gateway /v1/ws  （透传）
                          └─ /api/hedge/* ─bearer─> hedger /v1/*   （透传，可选）

hedger ──bearer──> gateway          hedger ──> PostgreSQL（自己的库）
gateway ──> exchange ──> sdk ──> 交易所
```

- 一个镜像，三个进程：`gateway`、`trader`（同时提供前端）、`hedger`（可选）。
- 镜像基于 `distroless/static:nonroot`，以 UID 65532 运行，根文件系统只读，丢弃全部 capability。
- gateway、hedger、PostgreSQL 只在内网监听，浏览器只访问 trader。

## Docker Compose

`compose.yaml` 固定了项目名与卷名，与仓库目录名无关：

| 卷 | 内容 |
|---|---|
| `keeluni-data` | gateway 的主密钥、加密账户文件、token；trader 的密码哈希；hedger 的 token 与数据库连接串 |
| `keeluni-pg` | PostgreSQL 数据 |
| `keeluni-postgres-secrets` | PostgreSQL 超级用户密码（只有 postgres 与 initdb 任务挂载） |

常用命令：

```sh
docker compose up -d --build --force-recreate   # 构建并（重新）启动
docker compose logs -f --tail=200               # 查看日志
docker compose down                             # 停止（卷保留）
make smoke                                      # 公共接口冒烟检查
```

### 通过代理访问交易所

```sh
KEELUNI_PROXY=http://host.docker.internal:7892 docker compose up -d
```

只有 gateway 访问交易所，代理只作用于 gateway。

### 不部署对冲应用

去掉 `compose.yaml` 中 trader 的 `HEDGER_URL` 与 `HEDGER_TOKEN_FILE` 两行，并不启动 `postgres`、`hedger-initdb`、`hedger` 服务即可。前端会自动隐藏「对冲」入口。

## 环境变量

### gateway

| 变量 | 默认 | 说明 |
|---|---|---|
| `GATEWAY_ADDR` | `127.0.0.1:8081` | 监听地址 |
| `GATEWAY_DATA_DIR` | `data/gateway` | 状态目录 |
| `GATEWAY_KEY_FILE` | `$DATA_DIR/master.key` | 主密钥（首次启动生成，0600） |
| `GATEWAY_TOKEN_FILE` | `$DATA_DIR/token` | 调用方使用的 bearer token（首次启动生成，0600） |
| `GATEWAY_LOG_LEVEL` | `info` | 日志级别 |
| `HTTPS_PROXY` / `HTTP_PROXY` | | 访问交易所的代理 |

### trader

| 变量 | 默认 | 说明 |
|---|---|---|
| `TRADER_ADDR` | `127.0.0.1:8080` | 监听地址 |
| `TRADER_DATA_DIR` | `data/trader` | 存放密码哈希 |
| `TRADER_WEB_DIR` | `web/dist` | 前端静态文件目录 |
| `TRADER_SECURE_COOKIE` | | 设为 `1` 时会话 cookie 带 `Secure`（前置 HTTPS 时使用） |
| `GATEWAY_URL` | `http://127.0.0.1:8081` | gateway 地址 |
| `GATEWAY_TOKEN_FILE` | `data/gateway/token` | gateway token |
| `HEDGER_URL` | | 设置后提供对冲应用 |
| `HEDGER_TOKEN_FILE` | `data/hedger/token` | hedger token |

### hedger

| 变量 | 默认 | 说明 |
|---|---|---|
| `HEDGER_ADDR` | `127.0.0.1:8082` | 监听地址 |
| `HEDGER_DATA_DIR` | `data/hedger` | 状态目录 |
| `HEDGER_TOKEN_FILE` | `$DATA_DIR/token` | 调用方使用的 bearer token（首次启动生成） |
| `HEDGER_DATABASE_URL` | | 数据库连接串；或用 `HEDGER_DATABASE_URL_FILE` |
| `HEDGER_DATABASE_URL_FILE` | `$DATA_DIR/database.url` | 存放连接串的文件（0600） |
| `GATEWAY_URL` / `GATEWAY_TOKEN_FILE` | | 同上 |

`hedger initdb` 用 `POSTGRES_ADMIN_URL`，或 `POSTGRES_HOST` + `POSTGRES_ADMIN_PASSWORD_FILE`（+ `POSTGRES_ADMIN_USER`，默认 `postgres`）以超级用户连接，创建 `hedger` 角色（随机密码）与 `hedger` 库（属主为 `hedger`，撤销 PUBLIC 的连接权限），并把连接串写入 `HEDGER_DATABASE_URL_FILE`。重复执行是安全的。

## 备份

| 必须备份 | 原因 |
|---|---|
| `master.key` | 没有它，`accounts.enc` 中的凭证无法解密，所有账户需要重新连接 |
| `accounts.enc` | 已连接账户的加密凭证 |
| hedger 数据库 | 对冲组、订单、成交、日志、资金费记录 |

gateway 的其余状态（标的目录、持仓快照、资金费率）都在内存中，重启后从交易所重建，不需要备份。

## 健康检查

| 接口 | 内容 |
|---|---|
| gateway `GET /v1/health`（免鉴权） | 各市场标的目录状态、常驻数据集（资金费率）采集状态、当前订阅主题数 |
| hedger `GET /v1/health`（免鉴权） | 数据库可用且 gateway 推送在线时为 `ok:true`，否则 503 并给出原因 |
| trader `GET /api/apps` | 是否提供对冲应用 |

## 远程访问

默认只绑定 `127.0.0.1`。需要远程访问时：

- 推荐 SSH 隧道或 Tailscale 等 VPN；
- 或者在前面放 TLS 反向代理，并设置 `TRADER_SECURE_COOKIE=1`。

不要把 gateway 或 hedger 的端口暴露到公网：它们只用 bearer token 鉴权，是给内部进程调用的。
