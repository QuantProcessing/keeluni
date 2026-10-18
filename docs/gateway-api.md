# Gateway / Trader API

本文是 `gateway/`（交易所接入服务）和 `trader/`（手动交易应用）的接口与安全约定。

## 0. 原则

gateway 的定义（判断一件事放 gateway 还是放应用，以这三条为准）：

1. **交易所的唯一出口。** 所有访问交易所的流量（公开/私有、REST/WebSocket）都经过 gateway；
   限流、连接、代理、凭证只存在于 gateway。同一交易所的额度统一分配并分优先级：
   下单/撤单 > 用户触发的查询 > 后台常驻采集（最多占该交易所额度的 25%）。应用不直连交易所。
2. **只提供交易所的当前事实。** 可以在内存中常驻持有（标的目录、持仓快照、资费等全市场数据集），
   但必须能在重启后从交易所重新拉回；不积累交易所给不回来的历史，不保存订单账本，
   不依赖数据库——应用的数据库故障时交易照常。
3. **做规范化，不做业务。** 统一各交易所的标识、单位与语义（例如资费统一为
   "单次结算比例 + 结算周期 + 年化"）；涉及两个标的/交易所之间的关系、任何策略、
   多笔订单的编排都属于应用。

因此常驻采集按"是否需要积累"划分：交易所的当前全市场事实是 gateway 的常驻数据集；
超出交易所保留期的时间序列、跨标的的计算，放在拥有自己数据库的应用里。

其他约定：

- 幂等只靠交易所 clientOrderId；结果 `unknown` 时先按 clientOrderId 查单，绝不盲目重下。
- 上层应用（trader、对冲、套利）各自拥有存储；trader 无数据库。
- 支持：Binance / OKX / Bybit / Bitget / Gate（现货 + U 本位永续），Hyperliquid / Lighter（现货 + 永续）。

## 1. 运行形态

```
browser ──HTTPS/cookie──> trader (Go, 单进程)
                           ├─ 静态文件 (web/dist)
                           ├─ /api/auth/*        登录/登出/会话
                           ├─ /api/gw/*  ──bearer──> gateway /v1/*   (透传)
                           └─ /api/ws    ──bearer──> gateway /v1/ws  (透传)

hedge / arb (未来) ──bearer──> gateway
gateway ──> exchange/ ──> sdk/ ──> 交易所
```

- gateway 只监听内网（compose 内部网络或 127.0.0.1），不对浏览器暴露。
- 容器数：gateway、trader 两个。

### 存储

gateway 唯一的持久化是 `$GATEWAY_DATA_DIR/accounts.enc`：每条凭证用 AES-256-GCM
单独加密，并以账户 id + 交易所作为附加数据绑定；原子写（tmp + fsync + rename），0600。
主密钥 `master.key` 与 bearer token `token` 首次启动自动生成（0600）。

## 2. 标识

**instrument id**：`{venue}:{product}:{symbol}`，例如
`binance:perp:BTC-USDT`、`bybit:perp:BTC-USDC`、`okx:perp:BTC-USDT-SWAP`、`hyperliquid:spot:HYPE-USDC`。
symbol 是 exchange 层的规范 symbol；Hyperliquid builder 永续带 dex 前缀（`hyperliquid:perp:xyz:TSLA-USDC`），
解析时只把前两个冒号当分隔符。

**account id**：gateway 生成的短 id（如 `acc_7f3k2m`）。账户分两类（`kind`）：

- `native`：一个交易所 + 一套凭证；spot / 各 perp 结算路由由 gateway 按交易所自动派生。
- `broker`：经纪账户（Gate CrossEx），一套凭证、一个保证金池，交易多个交易所的标的。

instrument id 永远是**订单簿所在交易所**的 id：经 CrossEx 交易 Binance BTC 永续仍是
`binance:perp:BTC-USDT`，与原生 Binance 账户交易的是同一个标的。账户决定"用谁的钱、走哪条通道"，
标的决定"哪个订单簿"。

行情永远来自订单簿所在交易所的原生接口，与用哪个账户交易无关。Kraken（USD 永续）、
Deribit（USDC 永续）与 Binance（USDC 永续、TradFi 永续）都是原生市场：可用原生账户交易，
CrossEx 支持的也可经 CrossEx 交易。`GET /v1/instruments` 带交易所给出的 `assetClass`
（`crypto` / `equity` / `commodity` / `fx` / `pre_ipo` ...，未知时省略）。

**clientOrderId**：调用方生成，十进制 uint48（`exchange` 层既有约束，各适配器已负责映射到
交易所原生格式，如 Hyperliquid cloid）。推荐生成方式：

```
id = (unix_ms << 7) | random(0..127)      // 2039 年前不溢出 uint48
```

Go client 和前端各提供一个 `newClientOrderId()`。

## 3. 下单语义

1. 调用方生成 clientOrderId，`POST .../orders`。
2. gateway 直接调交易所，**不重试、不落库**，返回三种结果之一：
   - `accepted`：交易所已接受，附 order 当前状态（市价单可能已 `filled`）
   - `rejected`：交易所明确拒绝，附 venue_code / venue_message
   - `unknown`：超时 / 断连 / 回包不可解析，结果未知
   经纪账户的 `accepted` 只表示经纪商已受理：订单此时为 `pending`，之后可能经 `account.orders`
   推送变为 `rejected`，附 `rejectCode`（经纪商风控标签）/ `rejectMessage`（交易所原文）。
3. `unknown` 时调用方**禁止**用新 id 重下，也**禁止**盲目用同 id 重下（各交易所对重复
   clientOrderId 的去重范围不一致，部分只在挂单中去重，实现时逐家核实），必须调用
   `GET .../orders/lookup?clientOrderId=` 确认。
4. 订单之后的状态变化通过 WS `orders` / `fills` 推送；交易所是唯一事实来源。

`exact lookup` 已在 7 个交易所实现（`exchange/internal/factoryclient/exact_lookup_*.go`）。
Lighter 通过扫描挂单与最近 100 条历史订单实现，频繁下单时极早的订单可能返回 `unconfirmed`。

HTTP 状态：accepted → 200，rejected → 422，unknown → 202；三者 body 都是 PlaceResult。

杠杆不再随下单设置；只有用户修改时调用 `PUT .../leverage`。

## 4. REST

鉴权：`Authorization: Bearer <GATEWAY_TOKEN>`（常量时间比较）。`/v1/health` 免鉴权。
所有数值用十进制字符串。

### 错误

所有交易所失败按同一套 `category` 分类，调用方无需了解具体交易所：

| category | 含义 | HTTP | retryable |
|---|---|---|---|
| `network` | 连不上交易所 / 超时 / 交易所 5xx | 502 | 是 |
| `rejected` | 交易所收到并拒绝（保证金不足、数量不合规…） | 422 | 否 |
| `credentials` | key、签名或权限被拒 | 422 | 否 |
| `rate_limited` | 交易所或本地限流 | 429 | 是 |
| `invalid` | 请求在发往交易所前即不合法 | 400 | 否 |
| `unsupported` | 交易所/市场不支持该操作 | 501 | 否 |
| `venue_error` | 交易所返回无法解析的内容 | 502 | 是 |

```json
{"error":{"code":"REJECTED","category":"rejected","message":"Margin is insufficient.",
          "venueCode":"-2019","venueMessage":"Margin is insufficient.","retryable":false}}
```

`venueMessage` 是交易所自己对拒绝原因的说明，原样透传（去除控制字符，最长 240 字符）；
只来自交易所业务错误体，绝不来自网络错误、代理页面、请求参数或凭证。
创建账户时若是 `network`，表示无法连接交易所，不代表 key 有误。

### 公共
```
GET  /v1/health
GET  /v1/venues                         交易所列表：{venue, kind:"native"|"broker", markets, fields}
GET  /v1/instruments?venue=&product=    规则：tick/step/minQty/minNotional
GET  /v1/orderbook?inst=&depth=20
GET  /v1/candles?inst=&interval=1m&start=&end=&limit=
GET  /v1/ticker?inst=                   last/mark/24h(由小时K派生)，资费字段来自常驻资费数据集
GET  /v1/funding?venue=&base=           常驻资费表（所有永续）+ 各市场采集状态；?inst= 取单个
GET  /v1/funding/history?inst=&since=&until=   已结算资费，窗口 ≤ 30 天（默认最近 7 天）
```

### 资费（常驻数据集）

统一口径（exchange 层换算，每家交易所含义相同）：

| 字段 | 含义 |
|---|---|
| `rate` | `nextFunding` 那次结算收付的仓位名义价值比例（小数），>0 表示多头付给空头 |
| `intervalHours` | 该标的自己的结算周期；无法确定时缺省，绝不默认 8h |
| `apr` | `rate × 8760 / intervalHours`（单利年化），跨交易所比较用；周期未知时缺省 |
| `nextFunding` | `rate` 对应的那次结算时刻（ms）；已过结算但新数据未到时按周期顺延 |
| `rateCap` / `rateFloor` | 交易所公布的上下限（有则给） |
| `stale` | 该市场的采集连续 3 个周期未成功 |

采集：Hyperliquid 用 WS `allDexsAssetCtxs`（一个订阅覆盖主 dex 和全部 HIP-3 dex，约 15 秒一次完整快照，
不占 REST 权重）；其余市场用全市场 HTTP，每 15 秒一次（Gate 30 秒）。后台采集最多占该交易所额度的
25%，拿不到额度就跳过本轮，不排队。读取失败保留上一份快照。各市场状态见 `/v1/health` 的 `datasets`。

各交易所的换算：Binance `fundingInfo` 只列非默认周期的标的，未列出的为 8h；OKX `fundingTime`
才是下次结算（`nextFundingTime` 是再下一次），周期为二者之差；Gate 周期以秒给出；Lighter REST 资费表是
8h 等效值（÷8），WS 是"百分数/小时"（÷100）；Hyperliquid、Lighter 每整点结算。

Trade 页的 `ticker` 推送和 `/v1/ticker` 的资费字段都从这个数据集读取，只有一个来源。

### 账户
```
GET    /v1/accounts                     [{id, venue, kind, label, createdAt, markets}]  —— 永不返回凭证
POST   /v1/accounts                     {venue, label, credentials:{...}}
                                        → 构造 client → 查余额校验 → 加密落盘 → 启动私有流
DELETE /v1/accounts/{id}
```

### 交易（均在 `/v1/accounts/{id}` 下）
```
GET    /balances                        经纪账户只有一个池：market 为 "crossex"
GET    /margin                          保证金池 {equity, available, initialMargin, maintenanceMargin,
                                         initialMarginRate, maintenanceMarginRate, unrealizedPnl,
                                         byVenue:[{venue, asset, equity, available, …}]}；原生账户为 null
GET    /instruments                     本账户可交易的标的及其下单规则
                                         [{inst, tickSize, stepSize, minQty, minNotional?}]
                                         经纪账户的规则是经纪商的，可能与交易所原生不同（如更细的步长）
GET    /positions                       来自 gateway 内存快照（见 §5），无快照时即时拉 REST
GET    /orders?inst=                    当前挂单
GET    /orders/history?inst=&limit=
GET    /fills?inst=&start=&limit=
POST   /orders                          {inst, side, type:"market"|"limit", qty, price?,
                                         tif:"gtc"|"ioc"|"post_only", reduceOnly, clientOrderId}
                                        → {status:"accepted"|"rejected"|"unknown", order?, category?,
                                           venueCode?, venueMessage?, message}
GET    /orders/lookup?inst=&clientOrderId=   | &orderId=
DELETE /orders/{orderId}?inst=
PUT    /leverage                        {inst, leverage}
```

## 5. WebSocket `/v1/ws`

单连接多订阅。客户端 → 服务端：

```json
{"op":"sub","ch":"book","inst":"binance:perp:BTC-USDT"}
{"op":"sub","ch":"candle","inst":"...","interval":"1m"}
{"op":"sub","ch":"account","account":"acc_7f3k2m"}
{"op":"sub","ch":"funding","inst":"okx:perp:BTC-USDT-SWAP"}
{"op":"unsub", ...同上}
{"op":"ping"}
```

服务端 → 客户端：`{"ch":"...","inst"|"account":"...","data":{...}}`

| ch | data | 说明 |
|---|---|---|
| `book` | `{bids:[[p,q]...], asks:[...], ts}` | gateway 维护本地簿，推 top-N **全量快照**，100ms 节流 |
| `trades` | `[{p,q,side,ts}]` | |
| `candle` | `{t,o,h,l,c,v}` | |
| `ticker` | `{mark,index,funding,fundingIntervalHours,fundingApr,nextFunding,oi,ts}` | 资费字段来自常驻资费数据集 |
| `funding` | 资费记录（同 `/v1/funding`） | 订阅时先发当前值，之后有变化才推；不开新的交易所连接 |
| `account.orders` | 单个订单的最新完整状态（可含 `rejectCode`、`rejectMessage`、`origin`: liquidation/adl/settlement） | upsert by orderId |
| `account.fills` | 单笔成交 | |
| `account.positions` | 该账户**全部持仓的完整列表** | 前端直接替换，无需合并 |
| `account.balances` | 该账户全部余额的完整列表 | 同上 |
| `account.status` | `{connected, reason}` | 私有流状态 |
| `account.margin` | 同 `GET .../margin` | 仅经纪账户；完整快照 |

**行情共享**：同一 `(ch, inst)` 在 gateway 内只开一个上游订阅，引用计数，最后一个订阅者离开后延迟 30s 关闭。

**持仓快照**：gateway 为每个账户维护内存持仓表——订阅开始/重连时 REST 拉全量，WS 增量更新，
每次变化推完整列表；收到成交、下单成功或私有流重连后 debounce 300ms 再拉一次 REST，
兜底不推持仓的交易所；另每 60 秒全量校正一次。新订阅者立即收到当前快照。
`GET .../positions` 在私有流在线时返回内存快照，否则实时读交易所。

**启动预热**：gateway 启动时为每个市场订阅一次盘口以建立公共 WebSocket 连接，
避免首个用户订阅承担数秒 TLS 握手。

## 6. Trader 安全设计

| 项 | 做法 |
|---|---|
| 密码 | 首次启动生成随机密码并打印一次；存 argon2id 哈希（`x/crypto/argon2`），可用 `trader passwd` 重置 |
| 会话 | 32 字节随机 token，服务端内存保存（重启需重新登录）；cookie `HttpOnly; SameSite=Strict; Path=/`，HTTPS 时 `Secure`；空闲 12h / 绝对 7d 过期 |
| CSRF | SameSite=Strict + 非 GET 请求校验 `Origin` 同源 + 要求自定义头 `X-Bolter: 1` |
| WS | 握手校验 cookie 与 `Origin` |
| 暴力破解 | 按 IP 指数退避（1s 起，上限 15min），全局失败计数 |
| 头 | CSP `default-src 'self'`、`X-Frame-Options: DENY`、`Referrer-Policy: no-referrer`、HSTS(HTTPS) |
| 网络 | 默认只绑 127.0.0.1；远程访问走 SSH 隧道 / Tailscale，或前置 TLS 反代 |
| 凭证 | 只写：浏览器 → trader → gateway → 加密落盘；任何响应、日志都不含凭证 |
| gateway | 只在内部网络；bearer token 存文件，权限 0600 |

## 7. 目录

```
sdk/          各交易所原生 API
exchange/     统一抽象（client、模型、目录、错误）
gateway/      venue · market · hub · account · store · ratelimit · resident · funding · api
trader/       登录、会话、反代、静态文件
web/          前端（设计规范见 DESIGN.md）
cmd/gateway   cmd/trader
```

## 8. 暂不支持（按需加回）

通知渠道（Telegram 等）、活动流 / 审计页、组合 ledger / PnL 归因、mock 模式。
