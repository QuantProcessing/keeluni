# KeelUni

一站式交易终端：自托管、单人使用，用一个终端交易你连接的每个交易所——现货、永续与期权，之后是大宗商品。

**文档：<https://quantprocessing.github.io/keeluni/>**（设计方案、实现细节与 API 参考）

```text
web        React 终端（交易 + 期权 + 对冲），默认中文、可切换英文
  │  同源 cookie 会话
trader     登录、会话、反向代理、提供前端                — 无数据库
  │  bearer token，仅内网
hedger     对冲应用：两条腿作为一个仓位                   — 自己的 PostgreSQL
optioner   期权应用：组合风险与多腿执行                   — 无数据库
  │  bearer token
gateway    与交易所无关的 REST + WebSocket API          — 只有一个加密文件
  │
exchange   规范化的客户端契约与模型
  │
sdk        各交易所原生 API
```

支持：Binance、OKX、Bybit、Bitget、Gate（现货 + USDT 永续；Binance/Bybit/Bitget 另有 USDC 永续，Binance 含 TradFi 永续）、Hyperliquid 与 Lighter（现货 + 永续）、Kraken（现货 + USD 永续）、Deribit（现货 + USDC 永续 + BTC/ETH 币本位与 USDC 本位期权）。Gate CrossEx 是一种经纪账户：一个保证金池交易 Binance、OKX、Bybit、Gate、Hyperliquid、Lighter、Kraken、Deribit 的永续，以及 Binance、OKX、Bybit、Gate、Deribit 的现货。

产品类别与期权的设计见[产品类别](https://quantprocessing.github.io/keeluni/architecture/products)。

## 运行

```sh
docker compose up -d --build --force-recreate
docker compose logs trader | grep password       # 首次启动的初始密码，只打印一次
```

打开 <http://127.0.0.1:8080>。本地开发（Go 1.26、Node 22）：`scripts/dev.sh`。详见[快速开始](https://quantprocessing.github.io/keeluni/guide/quick-start)与[部署与运维](https://quantprocessing.github.io/keeluni/guide/deploy)。

## 安全

每一笔订单都会进入真实交易所。请使用**没有提现权限**的 API Key，并从小额开始。凭证只写不读：经交易所验证后加密保存（`data/gateway/accounts.enc`，主密钥 `data/gateway/master.key`），任何接口与日志都不返回凭证。请备份 `master.key`。默认只绑定 `127.0.0.1`。详见[安全](https://quantprocessing.github.io/keeluni/guide/security)。

## 测试

```sh
make test
HEDGER_TEST_DATABASE_URL=postgres://postgres:pw@127.0.0.1:5432/postgres?sslmode=disable \
  go test ./pairexec/                            # 两腿执行内核对真实 PostgreSQL 的场景测试
```

界面规范见 [DESIGN.md](DESIGN.md)。
