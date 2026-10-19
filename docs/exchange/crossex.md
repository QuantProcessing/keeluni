# Gate CrossEx

Gate CrossEx 让**一套 API Key、一个保证金池**交易多个交易所的永续合约：Binance、OKX、Bybit、Gate、Hyperliquid、Lighter、Kraken、Deribit。各交易所之间的盈亏在同一个池里互抵，也一起被强平。

规格来源：Gate CrossEx API v1.0.6（2026-10-12）、CrossEx WebSocket v1.0.0、`github.com/gate/gateapi-go` v4.106.160 的 `api_cross_ex.go` / `model_crossex_*.go`。

## 已定决策

| # | 决策 |
|---|---|
| D1 | **标的身份不变，CrossEx 是一种账户。** `binance:perp:BTC-USDT` 无论经原生账户还是 CrossEx 账户交易都是同一个 instrument；不做独立的 CrossEx 页面，不做全局模式切换 |
| D2 | 同一交易所允许同时存在原生账户与 CrossEx 账户 |
| D3 | 只支持跨所模式 `CROSS_EXCHANGE`，分所模式账户拒绝接入 |
| D4 | 不提供修改账户模式、持仓模式、保证金模式的功能 |
| D5 | 股票 / ETF 不在范围内 |
| D7 | 只支持单向持仓 `SINGLE`；`DUAL` 账户接入时提示，下单在本地拒绝 |
| D8 | 不做跨账户持仓提示 |
| D9 | **行情永远来自订单簿所在交易所的原生接口**；CrossEx 只负责账户、下单、持仓与私有流 |

### 为什么不用 CrossEx 的公共行情

2026-10-18 实测 CrossEx 公共 WebSocket（`wss://api.gateio.ws/ws/crossex/public`）：

| 项 | 实测 |
|---|---|
| 数量单位 | **交易所原生单位透传**：OKX、Gate 为张，Binance、Hyperliquid 为币；而下单用币，`rule/symbols` 不给面值（`contract_size: null`） |
| 快照深度 | 各所上限不同：OKX ≤ 5 档、Bybit 1 档、Kraken 不支持快照 |
| 增量盘口 | 序列号语义各异，且没有 REST 快照可用于重新同步 |
| 历史 | 没有 K 线与资金费率历史 |

Kraken、Deribit、Binance USDC/TradFi 最初只能经 CrossEx 交易，后来为它们实现了原生 sdk 与适配器，CrossEx 公共行情路径随之删除。

## 标的映射

CrossEx symbol 格式 `{EXCHANGE}_{BUSINESS}_{BASE}_{QUOTE}`。映射按**交易所原生 symbol** 匹配，不按规范 symbol 拼接（各交易所规范化不一致，如 Binance `1000PEPE`、Hyperliquid `kPEPE`）。

| CrossEx | 原生 symbol | 市场 | 实测覆盖 |
|---|---|---|---|
| `BINANCE_FUTURE_{B}_USDT` | `{B}USDT` | `binance:perp:USDT` | 780/780（含 TradFi） |
| `BINANCE_FUTURE_{B}_USDC` | `{B}USDC` | `binance:perp:USDC` | 38/38 |
| `OKX_FUTURE_{B}_USDT` | `{B}-USDT-SWAP` | `okx:perp:USDT` | 494/494 |
| `BYBIT_FUTURE_{B}_USDT` | `{B}USDT` | `bybit:perp:USDT` | 797/797 |
| `BYBIT_FUTURE_{B}_USDC` | `{B}PERP` | `bybit:perp:USDC` | 68/68 |
| `GATE_FUTURE_{B}_USDT` | `{B}_USDT` | `gate:perp:USDT` | 1039/1039 |
| `HYPERLIQUID_FUTURE_{B}_USDC`，`tag=""` | `{B}` | `hyperliquid:perp:USDC` | 178/178 |
| `HYPERLIQUID_FUTURE_{B}_USDC`，`tag={dex}` | `{dex}:{B}` | `hyperliquid:perp:USDC` | xyz 111/111、para 22/22 |
| `LIGHTER_FUTURE_{B}_USDC` | `{B}` | `lighter:perp:USDC` | 193/193 |
| `KRAKEN_FUTURE_{B}_USD` | `PF_{B}USD`（`BTC` → `XBT`） | `kraken:perp:USD` | 205/205 |
| `DERIBIT_FUTURE_{B}_USDC` | `{B}_USDC-PERPETUAL` | `deribit:perp:USDC` | 142/142 |

- 映射在 CrossEx 规则表（`GET /crossex/rule/symbols`，公开）或原生标的目录刷新后重建。
- `state != live` 或映射不到的行不进入可交易集合，并计入目录状态便于观测。
- **OKX USDC 合约（240 个）未接入**：CrossEx 行情按张推送，下单用币，面值没有可靠来源，单位无法证明前不接入。

## 规则必须取自 CrossEx

实测 14 个 Gate 合约在 CrossEx 的 lot size 比原生更细（如 SOL 0.1 对原生 1 张 = 1 SOL）。因此**路由规则 = CrossEx 的** `tick_size / lot_size / min_size / min_notional / max_market_size / max_limit_size`，下单校验使用路由规则而非标的目录规则。CrossEx 数量单位是基础币，不乘合约面值。

## 各层实现

### sdk/crossex

- 手写客户端，不引入 `gateapi-go`（生成代码、体量大），把它作为规格与 fixture 来源。
- REST 签名与 Gate APIv4 相同。
- 私有 WebSocket `wss://api.gateio.ws/ws/crossex`：`login` 事件签名 `HMAC_SHA512(secret, "channel=&event=login&time={ts}")`；订阅 `order` / `usertrades` / `position` / `asset`，payload `["!all"]`；每 30 秒协议层 Ping；每 uid+channel 最多 20 条连接。
- 协议注意：订阅确认与数据推送都可能是 `event=subscribe`，以 payload 类型区分（数组是确认，对象是数据）；WebSocket 错误带 `label`，与 REST 同义。

### exchange：经纪型客户端

`factory.CrossExPerpConfig(key, secret, natives)` 返回普通的 `exchange.PerpClient`，同时实现 `exchange.BrokeredClient { Broker() Venue; Reaches() []MarketRoute }`。

- 它发布的标的的 `Identity.Venue` 是**底层交易所**，不是 CrossEx；只有 `NativeSymbol`（`BINANCE_FUTURE_BTC_USDT`）与 `Rules` 不同。
- `exchange.VenueCrossEx` 只作为客户端、凭证、限流、错误与私有流的归属，从不出现在 instrument id 中。
- 「适配器不得发布其他交易所的身份」的校验对经纪型客户端改为：允许发布 `Reaches()` 内的交易所。
- 行情方法（盘口、K 线、成交、资金费率）返回 `exchange.ErrUnsupported`：gateway 从不经账户客户端取行情。

订单语义映射：

| CrossEx | 规范化 |
|---|---|
| `POST /crossex/orders` 只返回 `order_id` | `AckAcceptedPending` |
| `NEW`（已通过 CrossEx 校验，待发往交易所） | `pending` |
| `OPEN` / `PARTIALLY_FILLED` / `FILLED` / `CANCELLED` | open / partially_filled / filled / canceled |
| `FAIL`（CrossEx 风控拒绝，`reason` 为 JSON `{label, message}`） | rejected，`rejectCode` = label |
| `REJECT`（交易所拒绝，`reason` 为原文） | rejected，`rejectMessage` = 原文 |
| `attribute` = LIQ / REDUCE / ADL / SETTLEMENT | `origin`：liquidation / adl / settlement |

- clientOrderId：uint48 十进制直接作为 `text`；重复时返回 `TRADE_ORDER_DUPLICATE_ERROR`（视为结果不明）。
- 精确查单：`GET /crossex/orders/{text}`；已结束订单可能查不到，回退扫描 `history_orders`，仍不能证明则 `unconfirmed`。
- 杠杆：`POST /crossex/positions/leverage {symbol, leverage}`，返回确认值。

### gateway

- 账户是一组 link（见[标识与路由](/architecture/identity#账户内部-link)）；CrossEx 账户一个 link 覆盖全部可达市场、一条私有流。
- 余额：CrossEx 返回单个保证金池，`Balance.Market = "crossex"`。
- 保证金快照 `GET /v1/accounts/{id}/margin` 与 `account.margin` 推送：`{equity, available, initialMargin, maintenanceMargin, initialMarginRate, maintenanceMarginRate, unrealizedPnl, byVenue[]}`。
- 账户创建校验：`GET /crossex/accounts` 成功且 `account_mode == CROSS_EXCHANGE`，否则拒绝并说明原因。
- 限流：`crossex` 一份预算（见[限流](/architecture/rate-limits)）。**这是一个用户跨全部底层交易所的单一预算。**
- 凭证：`crossex: AuthAPIKeySecret`。Key 需开启 CrossEx 权限，不需要也不应开启提现。

### 前端

- 账户行显示「Gate CrossEx · 跨所账户」；下单面板显示「最小下单」（CrossEx 自己的规则）。
- 可用余额是资金池的可用保证金。
- 订单 `pending` 显示「已受理 · 等待交易所确认」；转 `rejected` 时弹出提示，FAIL 显示 CrossEx 标签，REJECT 显示交易所原文。
- 账户概览显示保证金池：保证金余额、可用保证金、初始 / 维持保证金率，并按阈值告警：初始保证金率 < 110% 禁止划出；< 100% 自动撤单且只能平仓；维持保证金率 ≤ 100% 强平。下方按交易所列出权益与占用。
- 持仓与成交行内显示路由的交易所（`Gate CrossEx → Binance`）。

## 对冲中的 CrossEx

两条腿可以用同一个 CrossEx 账户：同一个保证金池支撑两条腿，界面只显示一次可用保证金，所需保证金按两腿合计与资金池比较。这是 CrossEx 最典型的用法之一：跨交易所对冲，不必在两个交易所各放一份保证金。
