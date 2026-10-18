# Gate CrossEx 接入设计

状态：设计稿（2026-10-18），分支 `feat/gate-crossex`。

规格来源：Gate CrossEx API v1.0.6（2026-10-12）、CrossEx WebSocket v1.0.0、
`github.com/gate/gateapi-go` v4.106.160 的 `api_cross_ex.go` / `model_crossex_*.go`。
文中"实测"指 2026-10-18 对线上公开接口的核对。

## 1. 已定决策

| # | 决策 |
|---|---|
| D1 | **标的身份不变，CrossEx 是一种账户。** `binance:perp:BTC-USDT` 无论经原生账户还是 CrossEx 账户交易都是同一个 instrument；"原生 vs CrossEx"由所选账户决定，不做独立 cross-trade 页面，不做全局模式切换。 |
| D2 | 同一交易所允许同时存在原生账户与 CrossEx 账户。 |
| D3 | 只支持跨所模式 `CROSS_EXCHANGE`。分所模式账户拒绝接入。 |
| D4 | 不提供修改账户模式、持仓模式、保证金模式的功能（沿用"无 SetXxxMode"原则）。 |
| D5 | 股票 / ETF 不在本需求内；后续作为独立需求，基于 gateway 设计独立服务端与前端。 |
| D6 | 本需求不做对冲 app。 |
| D7 | 只支持单向持仓 `SINGLE`。`DUAL` 账户接入时提示，下单在本地拒绝。 |
| D8 | 不做跨账户持仓提示。 |
| D9 | CrossEx 独有标的（约 840 个）作为后续独立需求。 |

## 2. 概念

| 概念 | 回答的问题 | 原生账户 | CrossEx 账户 |
|---|---|---|---|
| Instrument | 哪个订单簿 | `{venue}:{product}:{symbol}` | 同左，不变 |
| Account | 谁的钱、经哪条通道 | 1 凭证 → 1 交易所 | 1 凭证 → 多交易所 |
| Route | 账户 × 标的 → 下单规则 | 交易所原生规则 | CrossEx 规则（可能不同于原生） |
| Margin pool | 盈亏在哪里互抵、在哪里一起被强平 | 每个账户（市场）各自独立 | 一个账户一个共享池 |

行情（盘口、K 线、成交、资金费率、标记价）只属于 instrument，与账户无关。CrossEx 订单
真实进入底层交易所订单簿（交易所拒单原文透传、交易所维护时无法下单可证）。

行情来源按 instrument 选，不按账户选：

- 已有原生行情的标的（v1 全部）：继续用原生公共行情。
- CrossEx 独有标的（D9，后续）：把 CrossEx 公共 WS 当作普通行情源接入，不做特殊设计。

2026-10-18 实测 CrossEx 公共 WS（`wss://api.gateio.ws/ws/crossex/public`，免鉴权），这是 v1 仍用原生行情的原因：

| 项 | 实测 |
|---|---|
| 覆盖 | 8 个交易所；`order_book_x`、`order_book_update`、`trade`、`kline_x`、`funding_rate`、`mark_price` 均有推送 |
| 数量单位 | **交易所原生单位透传**：OKX、Gate 为张（OKX BTC 买一 974.48 与原生张数一致），Binance、Hyperliquid 为币。下单却用币，且 `rule/symbols` 不给面值（`contract_size: null`） |
| 快照深度 | 各所上限不同：OKX ≤ 5 档、Bybit 1 档、Kraken 不支持快照 |
| 增量盘口 | 序列号语义各异（Bybit `u=-1`，Hyperliquid 每条都是快照），且无 REST 快照可用于重同步 |
| 历史 | **无**。REST 只有 `market/tickers`、`market/funding_info`，需签名、限频 1 次/秒；没有 K 线与资金费率历史 |

CrossEx 账户 client 的行情方法（`OrderBook`、`Candles`、`PublicTrades`、行情 Watch、资金费率）
返回 `exchange.ErrUnsupported`：gateway 从不经账户 client 取行情，这是现有的方法级例外机制。

## 3. 范围

### 本期（v1）

- CrossEx **永续合约**（`business_type=FUTURE`），且仅限**能映射到现有原生 catalog 的标的**（见 §4）。
- 账户接入、保证金池、持仓、挂单、成交、下单、撤单、精确查单、杠杆设置、私有流。
- 前端：账户选择、按账户过滤标的、路由级下单规则、异步拒单提示、CrossEx 账户概览。

### 不在本期

| 项 | 原因 / 去向 |
|---|---|
| CrossEx 独有标的（Kraken 205、Deribit 142、Binance TradFi 217、Binance/OKX USDC 278，实测数） | 无原生行情来源，需要接入 CrossEx 公共 WS 作为行情源；后续需求 |
| 现货 `SPOT`、杠杆 `MARGIN` | 现货市价买入必须用 `quote_qty`；杠杆有借贷/利息模型。后续需求 |
| 资金划转、闪兑 | 账户管理能力，后续需求 |
| 股票 / ETF | D5 |
| 分所模式、模式修改 | D3、D4 |

## 4. 标的映射（CrossEx symbol ↔ instrument id）

CrossEx symbol 格式 `{EXCHANGE}_{BUSINESS}_{BASE}_{QUOTE}`。映射按**原生 native symbol** 匹配，
不按规范 symbol 拼接（各交易所规范化不一致，如 Binance `1000PEPE`、Hyperliquid `kPEPE`）。

| CrossEx | 原生 native symbol | 我们的 market | 实测覆盖 |
|---|---|---|---|
| `BINANCE_FUTURE_{B}_USDT` | `{B}USDT` | `binance:perp:USDT` | 563/563（另 217 TradFi 不在原生 catalog） |
| `OKX_FUTURE_{B}_USDT` | `{B}-USDT-SWAP` | `okx:perp:USDT` | 494/494（另 240 USDC 未服务） |
| `BYBIT_FUTURE_{B}_USDT` | `{B}USDT` | `bybit:perp:USDT` | 797/797 |
| `BYBIT_FUTURE_{B}_USDC` | `{B}PERP` | `bybit:perp:USDC` | 68/68 |
| `GATE_FUTURE_{B}_USDT` | `{B}_USDT` | `gate:perp:USDT` | 1039/1039 |
| `HYPERLIQUID_FUTURE_{B}_USDC`，`tag=""` | `{B}` | `hyperliquid:perp:USDC` | 178/178 |
| `HYPERLIQUID_FUTURE_{B}_USDC`，`tag={dex}` | `{dex}:{B}` | `hyperliquid:perp:USDC` | xyz 111/111、para 22/22 |
| `LIGHTER_FUTURE_{B}_USDC` | `{B}` | `lighter:perp:USDC` | 193/193 |

映射在 CrossEx 规则表（`GET /crossex/rule/symbols`，公开）或原生 catalog 刷新后重建；
`state!=live` 或映射不到的行不进入可交易集合，并计入 catalog 状态以便观测。

**规则必须取自 CrossEx。** 实测 14 个 Gate 合约在 CrossEx 的 lot size 比原生细
（如 SOL 0.1 vs 原生 1 张=1），因此 route 规则 = CrossEx 的 `tick_size / lot_size / min_size /
min_notional / max_market_size / max_limit_size`，下单校验使用 route 规则而非 catalog 规则。
CrossEx 数量单位是基础币，不乘合约面值。

## 5. 分层设计

### 5.1 sdk/crossex

手写客户端，不引入 `gateapi-go`（生成代码、体量大、依赖 `antihax/optional`），把它作为规格与
fixture 来源。

- REST：签名与 Gate APIv4 相同（复用 `sdk/gate` 的签名实现，或抽出共用包）。
- 私有 WS `wss://api.gateio.ws/ws/crossex`：`login` 事件，签名
  `HMAC_SHA512(secret, "channel=&event=login&time={ts}")`；订阅 `order` / `usertrades` /
  `position` / `asset`，payload `["!all"]`；每 30s 协议层 Ping；每 uid+channel 最多 20 连接。
- 协议坑：订阅确认与数据推送都可能是 `event=subscribe`，以 `payload` 类型（数组=确认，对象=数据）区分；
  WS 错误带 `label`，与 REST 同义。
- 本期用到的接口：`rule/symbols`、`accounts`、`positions`、`open_orders`、`orders`（POST /
  GET {order_id|text} / DELETE）、`history_orders`、`history_trades`、`positions/leverage`（GET/POST）、`fee`。

### 5.2 exchange 层：CrossEx 是"经纪型"客户端

`factory.CrossExPerpConfig(key, secret, ...)` 返回 `exchange.PerpClient`。与现有客户端的
唯一本质区别：**它返回的 instrument 的 `Identity.Venue` 是底层交易所，而不是客户端自己。**

- 新增 `exchange.VenueCrossEx`，只作为客户端 / 凭证 / 限流 / 错误归属的 venue，不出现在 instrument id 中。
- 新增可选接口 `exchange.BrokeredClient { Broker() Venue; Reaches() []MarketRoute }`。
  现有"适配器不得发布其他 venue 的 identity"的校验改为：经纪型客户端允许发布 `Reaches()` 内的 venue。
  这是对 `docs/exchange/README.md` 不变量的显式扩展，需要同步更新契约测试与文档。
- `Identity.NativeSymbol` = CrossEx symbol（如 `BINANCE_FUTURE_BTC_USDT`），
  `Identity.Symbol` = 对应原生 instrument 的规范 symbol，`Rules` = CrossEx 规则。
- 映射（§4）放在 factoryclient，输入是原生 catalog，输出是经纪 instrument 列表。

订单语义映射：

| CrossEx | 归一化 |
|---|---|
| `POST /crossex/orders` 只返回 `order_id` | `AckAcceptedPending`（已有状态） |
| `NEW`（已通过 CrossEx 校验，待发往交易所） | `pending`（不能沿用通用映射 `new→open`） |
| `OPEN` / `PARTIALLY_FILLED` / `FILLED` / `CANCELLED` | open / partially_filled / filled / canceled |
| `FAIL`（CrossEx 风控拒绝，`reason` 为 JSON `{label,message}`） | rejected，code=label |
| `REJECT`（交易所拒绝，`reason` 为原文） | rejected，message=原文 |
| `attribute` = LIQ / REDUCE / ADL / SETTLEMENT | 透传，前端标记非本人发起 |

- `ClientOrderID`：uint48 十进制直接作为 `text`（字母数字、`-_`、<64 字符），重复返回
  `TRADE_ORDER_DUPLICATE_ERROR`。
- 精确查单：`GET /crossex/orders/{text}`；文档注明"已结束订单可能无法再用 text 查询"，
  查不到时回退扫描 `history_orders`，仍不能证明则返回 `unconfirmed`。
- 持仓模式：只支持 `SINGLE`（`position_side=NONE`）。`DUAL` 账户在接入时提示，下单在本地拒绝。
- 杠杆：`POST /crossex/positions/leverage {symbol, leverage}`，返回确认值。

### 5.3 gateway

**账户：从"一个账户 = 一个交易所"改为"一个账户 = 一组 link"。**

```
runtime
  links []link
link
  client   exchange.PerpClient / SpotClient
  markets  []venue.Market          // 原生：1 个；CrossEx：它能到达的全部 perp market
  routes   map[InstrumentID]Route  // 本账户可交易的标的及其规则
  resolve  nativeSymbol → InstrumentID
```

- 原生账户：每个 market 一个 link，行为不变。
- CrossEx 账户：一个 link 覆盖多个 market，**一条私有 WS**，事件按 symbol 分发到 instrument；
  持仓 REST 一次读全量后按 market 分区。
- `clientFor(instrument)`：从"`owner.Venue == rt.venue`"改为"某个 link 的 routes 包含该 instrument"。
- 余额：CrossEx 返回单个保证金池，`Balance.Market = "crossex"`；`assets[]` 作为按交易所的明细。
- 新增保证金快照（通用模型，原生账户后续也可填）：
  `{equity, available, initialMargin, maintenanceMargin, imr, mmr, byVenue[]}`。
- 账户创建校验：`GET /crossex/accounts` 成功，且 `account_mode == CROSS_EXCHANGE`，否则拒绝并说明原因（D3）。

**限流**：新增 `crossex` venue 预算。下单 / 撤单 / 改杠杆共享 100 次 / 10s（REST 与 WS 共用），
查询 200 次 / 10s，单用户最多 1000 挂单。注意这是**一个用户跨全部底层交易所的单一预算**。

**凭证**：`credential` 注册 `crossex: AuthAPIKeySecret`。Key 需开启 CrossEx 权限；不需要也不应开启提现。

### 5.4 Gateway API 变更

```
GET /v1/venues
  + {venue:"crossex", kind:"broker", markets:[...可到达的 market...], fields:[apiKey, apiSecret]}

GET /v1/accounts
  + kind: "native" | "broker"
  markets: 本账户可到达的 market 列表（CrossEx 为多个）

GET /v1/accounts/{id}/instruments          新增
  → [{inst, tickSize, stepSize, minQty, minNotional?, maxMarketQty?, maxLimitQty?}]
    本账户可交易的标的及路由规则；原生账户返回其 market 的 catalog 规则

GET /v1/accounts/{id}/margin               新增；WS 频道 account.margin
  → {equity, available, initialMargin, maintenanceMargin, imr, mmr, byVenue:[...]}

Order 增加
  + rejectCode?, rejectMessage?            异步拒单原因
  + origin?: "liquidation" | "adl" | "settlement"
```

下单语义保持不变：`accepted` 对 CrossEx 意味着"CrossEx 已受理"，订单此时为 `pending`，
之后可能经 `account.orders` 推送转为 `rejected`。这不是 CrossEx 专用分支——所有返回
`AckAcceptedPending` 的交易所都适用同一前端处理。

## 6. 前端

- **账户选择器**：账户行显示类型，如 `CrossEx · main（跨所）`、`Binance · main`。
- **标的选择器**：默认列出当前账户可交易的标的；不可交易的保持可见但置灰，标注"可经 X 账户交易"。
  选中当前账户不可交易的标的时，下单区显示原因与可用账户的切换按钮。**不自动切换账户**（资金归属必须由用户显式决定）。
- **下单框**：显示路由，如 `Binance 永续 · 经 CrossEx`；数量步长、最小量、最小名义额、市价最大量使用
  `/accounts/{id}/instruments` 的路由规则；可开数量基于保证金池 `available`。
- **订单生命周期**：`pending` 显示"已受理，等待交易所"；转 `rejected` 时弹出提示，FAIL 显示
  CrossEx label 与说明，REJECT 显示交易所原文。
- **账户概览（CrossEx）**：保证金余额、可用保证金、初始 / 维持保证金率，并按阈值告警：
  IMR < 110% 禁止划出；IMR < 100% 自动撤单且只能平仓；MMR ≤ 100% 强平。下方按交易所列出权益与占用。
- **持仓 / 订单 / 成交**：行内显示交易所标记；强平接管、ADL、下架清算的订单单独标记。

## 7. 实施顺序

| 阶段 | 内容 | 完成标准 |
|---|---|---|
| P1 | `sdk/crossex`：REST + 私有 WS | 基于官方文档样例的 fixture 测试；签名向量测试 |
| P2 | exchange：经纪型客户端、映射、订单语义 | 契约测试（含经纪型 identity 例外）；映射覆盖率测试使用实测快照 |
| P3 | gateway：link 模型、路由规则、保证金快照、限流、凭证、API | 现有账户行为回归不变；CrossEx 账户 runtime 单测 |
| P4 | web：账户 / 标的 / 下单框 / 异步拒单 / 概览 | 交互测试；小额实盘验收 |

## 8. 待确认

无。原待确认项已定为 D7–D9。
