# 快速开始

## 用 Docker 运行（推荐）

需要 Docker 与 Docker Compose。

```sh
git clone https://github.com/QuantProcessing/keeluni.git
cd keeluni
docker compose up -d --build --force-recreate
docker compose logs trader | grep password    # 首次启动的初始密码，只打印一次
```

打开 <http://127.0.0.1:8080>，用初始密码登录。

compose 会启动：

| 服务 | 作用 | 对外端口 |
|---|---|---|
| `gateway` | 交易所接入 | 无（仅内网） |
| `trader` | 登录、提供前端、反向代理 | `127.0.0.1:8080` |
| `postgres` | hedger 的数据库 | 无 |
| `hedger-initdb` | 首次创建 hedger 的数据库角色与库（一次性） | 无 |
| `hedger` | 对冲应用 | 无 |

只有 `127.0.0.1:8080` 对本机开放，其余服务只在 compose 内网可达。

## 本地运行（开发）

需要 Go 1.26 与 Node 22。

```sh
scripts/dev.sh                                    # http://127.0.0.1:8080
HTTPS_PROXY=http://127.0.0.1:7892 scripts/dev.sh  # 交易所需要代理时
```

`scripts/dev.sh` 会构建前端与三个二进制，启动 gateway 与 trader。对冲应用需要一个 PostgreSQL，第一次运行时提供超级用户连接串，它会创建自己的角色和库，并把连接串保存到 `data/hedger/database.url`：

```sh
POSTGRES_ADMIN_URL=postgres://postgres:pw@127.0.0.1:5432/postgres?sslmode=disable scripts/dev.sh
```

之后直接运行 `scripts/dev.sh` 即可。没有数据库时只启动 gateway 和 trader。

## 连接第一个交易所

1. 点击右上角 **连接**（已有账户时在账户菜单里选择 **+ 连接交易所**）。
2. 选择交易所，按提示填写 API Key 等信息。每个交易所需要的字段不同，表单会说明在哪里创建密钥、需要哪些权限。
3. 提交后，gateway 会先向交易所查询一次余额来验证密钥，通过后才加密保存。

::: tip API 权限
只开启交易与读取权限，**不要开启提现**。Gate CrossEx 需要开启 CrossEx 交易权限，账户必须处于跨所模式、单向持仓。
:::

## 修改密码

```sh
go run ./cmd/trader passwd                                  # 本地
docker compose run --rm -it --no-deps trader passwd         # Docker
docker compose restart trader                               # 让新密码生效并退出所有会话
```

密码以 argon2id 哈希保存，**无法找回**，忘记时只能用上面的命令重置。

## 检查运行状态

```sh
make smoke     # 对运行中的 compose 栈做公共接口检查：每个市场的行情、盘口、K 线（不下单）
```
