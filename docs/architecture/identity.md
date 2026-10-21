# 标识与路由

KeelUni 把三个经常被混为一谈的概念分开：

| 概念 | 回答的问题 | 例子 |
|---|---|---|
| **Instrument（标的）** | 哪个订单簿 | `binance:perp:BTC-USDT` |
| **Account（账户）** | 谁的钱、经哪条通道 | Binance 原生账户 `acc_7f3k2m`；Gate CrossEx 账户 `acc_9x2q1p` |
| **Route（路由）** | 这个账户交易这个标的，用什么规则 | CrossEx 交易 Gate SOL 永续时步长 0.1 SOL（原生是 1 张 = 1 SOL） |

外加一个只对经纪账户有意义的概念：

| **Margin pool（保证金池）** | 盈亏在哪里互抵、在哪里一起被强平 | 原生账户：每个账户（市场）各自独立；CrossEx：一个账户一个共享池 |
|---|---|---|

## instrument id

格式 `{venue}:{product}:{symbol}`：

```
binance:perp:BTC-USDT          Binance U 本位永续
binance:perp:BTC-USDC          Binance USDC 永续（与 USDT 永续同一个账户）
okx:perp:BTC-USDT-SWAP         OKX 永续
hyperliquid:spot:HYPE-USDC     Hyperliquid 现货
hyperliquid:perp:xyz:TSLA-USDC Hyperliquid HIP-3 builder 永续（带 dex 前缀）
kraken:perp:BTC-USD            Kraken Futures（原生 PF_XBTUSD）
deribit:perp:BTC-USDC          Deribit（原生 BTC_USDC-PERPETUAL）
kraken:spot:BTC-USD            Kraken 现货（原生 BTC/USD，即 WebSocket v2 名称）
deribit:spot:BTC-USDC          Deribit 现货（原生 BTC_USDC）
deribit:option:BTC-USD-20261127-73000-P   Deribit 币本位期权（原生 BTC-27NOV26-73000-P）
deribit:option:SOL-USDC-20261023-75-C     Deribit U 本位期权（原生 SOL_USDC-23OCT26-75-C）
okx:option:BTC-USDM-20261106-82000-P      OKX U 本位期权（原生 BTC-USD_UM-261106-82000-P）
```

期权的规范 symbol 见[产品类别](./products#标的身份)。

- `symbol` 是 exchange 层的规范 symbol；交易所原生名称（`PF_XBTUSD`、`BTC_USDC-PERPETUAL`）保存在标的的 `NativeSymbol` 中，不出现在 id 里。
- 解析时只把前两个冒号当作分隔符，所以 HIP-3 的 `xyz:TSLA-USDC` 能完整保留。
- **id 永远是订单簿所在交易所的。** 经 CrossEx 交易 Binance BTC 永续，标的仍是 `binance:perp:BTC-USDT`；`crossex` 永远不会出现在 instrument id 中。

## 市场（market）

市场是一个「交易所 × 产品 × 结算币」的路由，名称 `venue:product[:settle]`：

| 市场 | 说明 |
|---|---|
| `binance:perp:USDT` / `binance:perp:USDC` / `binance:spot` | Binance 的 USDT 与 USDC 永续是同一个 U 本位账户：一个客户端、一条私有流、余额都记在 `binance:perp:USDT` 下 |
| `okx:perp:USDT` / `okx:spot` | |
| `bybit:perp:USDT` / `bybit:perp:USDC` / `bybit:spot` | |
| `bitget:perp:USDT` / `bitget:perp:USDC` / `bitget:spot` | |
| `gate:perp:USDT` / `gate:spot` | |
| `hyperliquid:perp:USDC` / `hyperliquid:spot` | |
| `lighter:perp:USDC` / `lighter:spot` | |
| `kraken:perp:USD` / `kraken:spot` | 期货与现货是 Kraken 的两个平台、两套 Key；一个账户可填任一组或两组 |
| `deribit:perp:USDC` / `deribit:spot` / `deribit:option` | 一个 Key、一组币种钱包；BTC、ETH 币本位与 USDC 本位期权是**一个**期权市场 |
| `okx:spot` / `okx:perp:USDT` / `okx:option` | 一个交易账户；币本位（BTC-USD）与 U 本位（BTC-USD_UM）期权是**一个**期权市场；余额按交易所列一次（「OKX 统一账户」） |

`gateway/venue/venue.go` 中的 `Markets` 列表就是 gateway 服务的全部范围。每个市场都有自己的公共连接器，负责标的目录与行情。

## 账户

账户 id 由 gateway 生成（如 `acc_7f3k2m`）。两类：

| kind | 含义 | markets |
|---|---|---|
| `native` | 一个交易所 + 一套凭证；现货与各永续结算路由由 gateway 按交易所自动派生 | 该交易所的市场 |
| `broker` | 经纪账户（Gate CrossEx）：一套凭证、一个保证金池，交易多个交易所的标的 | 它能到达的全部永续与现货市场 |

### 账户内部：link

gateway 内部把一个账户表示为一组 **link**：

```
account
  links []link
link
  client   exchange.PerpClient、SpotClient 或 OptionClient
  markets  这个 client 交易的市场（原生：通常 1 个；Binance U 本位：USDT+USDC；CrossEx：全部可达市场）
  routes   本账户可交易的标的 → 下单规则
  resolve  交易所原生 symbol → instrument id
```

- 原生账户：每个市场（或共享账户的一组市场）一个 link。
- CrossEx 账户：一个 link 覆盖所有可达市场，**一条私有 WebSocket**，事件按 symbol 分发到各个标的；持仓 REST 一次读全量后按市场分区。
- 给定一个 instrument，「这个账户能不能交易它」= 某个 link 的 routes 包含它。

## 账户能否交易一个标的

前端与 hedger 用同一个判断：

1. 账户处于启用状态；
2. 账户的 `markets` 包含标的所在市场（`marketOf(instrument)`）；
3. 账户的路由列表（`GET /v1/accounts/{id}/instruments`）包含该标的。

第 3 条对经纪账户尤其重要：CrossEx 到达一个市场，但不一定覆盖市场里的每一个标的。

## 路由规则

`GET /v1/accounts/{id}/instruments` 返回 `[{inst, tickSize, stepSize, minQty, minNotional?}]`：

- 原生账户：就是标的目录中的交易所规则。
- 经纪账户：经纪商自己的规则。实测 14 个 Gate 合约在 CrossEx 的步长比原生更细（如 SOL 0.1 对原生 1 张 = 1 SOL），CrossEx 还有自己的最小金额。

下单面板、下单校验、hedger 的数量计算都使用路由规则，而不是标的目录的规则。

## clientOrderId

调用方生成，十进制 uint48 字符串（exchange 层的约束，各适配器负责映射到交易所原生格式，如 Hyperliquid 的 cloid、CrossEx 的 `text`、Deribit 的 label）。推荐生成方式：

```
id = (unix_ms << 7) | random(0..127)      // 2039 年前不超过 uint48
```

Go 客户端（`gwclient.NewClientOrderID`）与前端（`newClientOrderId`）各有一份实现。
