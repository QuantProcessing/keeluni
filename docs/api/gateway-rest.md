# gateway REST

基址 `/v1`，鉴权 `Authorization: Bearer <GATEWAY_TOKEN>`（`/v1/health` 除外）。经 trader 访问时为 `/api/gw/v1`。

## 公共

### GET /v1/health

免鉴权。

```json
{"ok": true,
 "markets": [{"market":"binance:perp:USDT","instruments":780}, {"market":"…","instruments":0,"error":"…"}],
 "datasets": [{"dataset":"funding","market":"okx:perp:USDT","mode":"poll","intervalMs":15000,
               "updatedAt":1792392071143,"stale":false}, …],
 "topics": 42}
```

`markets[].error` 是该市场最近一次加载失败的原因（仍保留上一份成功快照）；`datasets[].mode` 为 `poll` 或 `stream`。

### GET /v1/venues

可连接的交易所：

```json
[{"venue":"okx","kind":"native","markets":["okx:perp:USDT","okx:spot"],
  "fields":[{"key":"apiKey","label":"API Key","required":true}, {"key":"apiSecret",…}, {"key":"passphrase",…}]},
 {"venue":"crossex","kind":"broker","markets":["binance:perp:USDT", "okx:perp:USDT", …],"fields":[…]}]
```

`fields` 描述创建账户时需要的凭证字段：`metadata: true` 的是非机密信息（如 Hyperliquid 账户地址、Lighter 序号），`valueType: "integer"` 的是整数。

### GET /v1/instruments?venue=&product=

标的目录：

```json
[{"id":"okx:perp:BTC-USDT-SWAP","venue":"okx","product":"perp","symbol":"BTC-USDT-SWAP",
  "base":"BTC","quote":"USDT","settle":"USDT",
  "tickSize":"0.1","stepSize":"0.0001","minQty":"0.0001","minNotional":"",
  "assetClass":"crypto"}]
```

`assetClass`：`crypto` / `equity` / `commodity` / `fx` / `index` / `pre_ipo` / `rwa_other`，未知时省略。

`product`：`spot` / `perp` / `option`。**期权只在 `product=option` 时列出**（数千个合约属于期权链，不属于交易页的市场选择器）。期权另有：

```json
{"id":"deribit:option:BTC-USD-20261127-73000-P","product":"option","base":"BTC","quote":"USD","settle":"BTC",
 "tickSize":"0.0001","stepSize":"0.1","minQty":"0.1","coarseTickFrom":"0.005","coarseTick":"0.0005",
 "option":{"underlying":"BTC","kind":"put","strike":"73000","expiry":1795766400000,"premiumAsset":"BTC","inverse":true,"european":true}}
```

价格不低于 `coarseTickFrom` 时最小变动价位为 `coarseTick`。

### GET /v1/orderbook?inst=&depth=20

```json
{"bids":[["82817.3","0.5"], …],"asks":[["82817.4","1.2"], …],"ts":1792392071143}
```

### GET /v1/candles?inst=&interval=1m&end=&limit=

`interval`：`1m` `5m` `15m` `30m` `1h` `4h` `12h` `1d`。返回 `[{t,o,h,l,c,v}]`，按时间升序。

### GET /v1/ticker?inst=

```json
{"last":"82817.3","open24h":"…","high24h":"…","low24h":"…","volume24h":"…",
 "mark":"…","funding":"0.0001","fundingIntervalHours":8,"fundingApr":"0.1095","nextFunding":1792396800000}
```

24 小时统计由小时 K 线派生；资金费率字段来自常驻数据集。

### GET /v1/markets

市场列表（交易页的标的选择器）：所有现货与永续标的的最新价、24 小时涨跌、24 小时成交额，以及资金费率和持仓额。按 24 小时成交额降序排列，支持 gzip（约 13k 行）。

```json
{"inst":"okx:perp:BTC-USDT-SWAP","last":"83741.2","open24h":"81470","change24h":0.027878,
 "volume24h":6566440687,"fundingRate":"0.0000313","fundingApr":"0.0342741","oiUsd":2613720245}
```

- 数据来自各交易所的全市场 24h 行情接口，每个交易所的现货、永续各读一次（永续一次读取覆盖 USDT 和 USDC；Bitget 读两个品类；Hyperliquid 为主 dex 加每个 builder dex 各一次），每 60 秒读取一次（Hyperliquid 永续每次 12 个请求，3 分钟一次），只占用后台采集的限额份额（各交易所预算的 1/4）。在 `/v1/health` 的 `datasets` 中以 `tickers`、`{venue}:{product}` 出现。
- `change24h` 为小数（0.025 即 +2.5%）。Kraken 现货的开盘价是当日 UTC 0 点的开盘价，不是 24 小时前。
- `volume24h` 为 24 小时成交额，按计价资产折算为美元：稳定币按 1，其他计价资产按其成交额最大的稳定币现货对折算（BTC-USDT 给出 BTC，USDT-IDR 给出 IDR）。无法折算或交易所未提供时省略。
- 交易所没有覆盖的标的仍会列出，只是没有价格字段。

### GET /v1/funding

| 参数 | 返回 |
|---|---|
| `?inst=` | 单个标的的资金费率 |
| `?venue=&base=` | 资金费率表（可按交易所、底层币筛选）+ 各市场采集状态 |

```json
{"inst":"okx:perp:BTC-USDT-SWAP","venue":"okx","base":"BTC","rate":"0.0000408","intervalHours":8,
 "apr":"0.0447","nextFunding":1792396800000,"mark":"82766.1","oiUsd":"2453315740",
 "observedAt":1792392071143,"stale":false}
```

字段含义见[资金费率](/architecture/funding)。`mark` 是标记价，`oiUsd` 是按标记价折算的持仓量（美元）；交易所的批量接口不提供时省略（Binance 的持仓量只能逐个标的查询，Hyperliquid 的流不带标记价）。对冲页的"套利机会"用它过滤没有深度的市场。

### GET /v1/options

每个期权系列（交易所 × 标的 × 权利金资产）的概要：

```json
[{"series":"deribit:BTC:BTC","venue":"deribit","market":"deribit:option","underlying":"BTC","premiumAsset":"BTC","strikeAsset":"USD",
  "inverse":true,"index":81432.74,"expiries":11,"contracts":880,"frontAtmIv":0.313,"stale":false}]
```

### GET /v1/options/chain?series=&expiry=

一个系列的完整期权链；`expiry`（毫秒）只返回一个到期日的行。

```json
{"series":"deribit:BTC:BTC", "underlying":"BTC", "premiumAsset":"BTC", "strikeAsset":"USD", "inverse":true, "index":81432.74,
 "updatedAt":1792495100337, "stale":false,
 "expiries":[{"expiry":1792828800000,"forward":81465.31,"discount":1,"atmIv":0.3163,"rr25":-0.037,"bf25":0.0104,"strikes":27}],
 "rows":[{"inst":"deribit:option:BTC-USD-20261024-81000-C","kind":"call","strike":"81000","expiry":1792828800000,
          "bid":"0.0155","ask":"0.0165","mark":"0.0161","markIv":0.3206,"bidIv":0.3039,"askIv":0.3411,
          "delta":0.5744,"gamma":0.0000731,"vega":32.84,"theta":-136.33,"oi":"7.3","volume":"12.4"}]}
```

价格为权利金资产；IV 为小数；希腊值为每单位标的、行权价币种。每个到期日的 `forward` 与 `discount` 就是该到期日的定价模型：期权价值 = `discount × Black-76(forward)`；交易所发布远期时（Deribit、OKX）`discount = 1`，按现货加利率定价时（Binance）为 e^{-rT}。`stale` 为真表示该市场的采集错过了三个周期。未知系列 404 `UNKNOWN_SERIES`，尚未加载 503 `NO_CHAIN`。

### GET /v1/funding/history?inst=&since=&until=

已结算资金费率，窗口最长 30 天（默认最近 7 天）：`[{t, rate, intervalHours?, apr?}]`。不提供历史的交易所返回 `unsupported`。

## 账户

### GET /v1/accounts

```json
[{"id":"acc_7f3k2m","venue":"okx","kind":"native","label":"main","createdAt":"2026-10-01T00:00:00Z",
  "markets":["okx:perp:USDT","okx:spot"],"active":true}]
```

**永远不返回凭证。** `kind` 为 `native` 或 `broker`。

### POST /v1/accounts

```json
{"venue":"okx","label":"main","credentials":{"apiKey":"…","apiSecret":"…","passphrase":"…"}}
```

构造客户端 → 在每个市场查询余额验证 → 加密保存 → 启动私有流。成功返回账户；失败按 category 返回。

### DELETE /v1/accounts/{id}

删除账户与其加密凭证，关闭私有流。

## 交易（`/v1/accounts/{id}` 下）

### GET /balances

`[{market, asset, total, available, locked}]`。经纪账户只有一个池，`market` 为 `crossex`；Binance U 本位的余额记在 `binance:perp:USDT` 下。

### GET /margin

保证金池；原生账户为 `null`：

```json
{"equity":"10105","available":"8600","initialMargin":"1400","maintenanceMargin":"700",
 "initialMarginRate":"7.2","maintenanceMarginRate":"14.4","unrealizedPnl":"57",
 "byVenue":[{"venue":"binance","asset":"USDT","equity":"6505","available":"5600","initialMargin":"200"}],
 "ts":1792392071143}
```

保证金率是「保证金余额 ÷ 保证金」的倍数（7.2 = 720%）。

### GET /instruments

本账户可交易的标的与下单规则（路由）：

```json
[{"inst":"okx:perp:BTC-USDT-SWAP","tickSize":"0.1","stepSize":"0.00001","minQty":"0.00001","minNotional":"5"}]
```

支持止盈止损的市场另带 `"triggers":{"priceTypes":["last","mark"]}`，见[止盈止损](#止盈止损触发单)。

经纪账户的规则是经纪商的，可能与交易所原生不同。

### GET /positions

```json
[{"inst":"binance:perp:BTC-USDT","side":"long","size":"0.05","entryPrice":"80100","markPrice":"81100",
  "unrealizedPnl":"50","liquidationPrice":"…","leverage":"10","margin":"405"}]
```

`size` 总为正，`side` 为 `long` / `short`。私有流在线时返回内存快照，否则实时读交易所。

### GET /orders?inst=

当前挂单：

```json
[{"inst":"…","orderId":"…","clientOrderId":"…","side":"buy","type":"limit","price":"79000","qty":"0.01",
  "filled":"0","avgPrice":"","tif":"gtc","reduceOnly":false,"status":"open",
  "rejectCode":"","rejectMessage":"","origin":"","createdAt":…,"updatedAt":…}]
```

`status`：`pending`（经纪商已受理、交易所未确认）/ `open` / `partially_filled` / `filled` / `canceled` / `rejected`。`origin`：`liquidation` / `adl` / `settlement`（风控订单）。

### GET /fills?inst=&start=&limit=

`[{inst, fillId, orderId, clientOrderId, side, price, qty, fee, feeAsset, liquidity, ts}]`

### POST /orders

```json
{"inst":"okx:perp:BTC-USDT-SWAP","side":"buy","type":"limit","qty":"0.01","price":"80000",
 "tif":"post_only","reduceOnly":false,"clientOrderId":"229736271958528"}
```

| 字段 | 说明 |
|---|---|
| `type` | `market` / `limit` |
| `tif` | 限价单：`gtc` / `ioc` / `post_only` |
| `reduceOnly` | 只对永续有效 |

返回 `PlaceResult`：

```json
{"status":"accepted","order":{…}}
{"status":"rejected","category":"rejected","venueCode":"-2019","venueMessage":"Margin is insufficient.","message":"…"}
{"status":"unknown","message":"…"}
```

HTTP：accepted 200、rejected 422、unknown 202。

### GET /orders/lookup?inst=&clientOrderId=

或 `&orderId=`。返回 `{status:"found"|"not_found"|"unconfirmed", order?}`。

### DELETE /orders/{orderId}?inst=

撤单。

### 止盈止损（触发单）

止盈 / 止损是挂在交易所的只减仓市价单：价格触及触发价时由交易所发出。只有 `GET /instruments` 中带 `triggers` 的市场支持（`{"triggers":{"priceTypes":["last","mark"]}}`，列出可用的触发价类型，第一个为默认）；不支持的市场（如 Gate CrossEx）只能限价或市价平仓。目前已接入：

| 交易所 | 触发价 | 说明 |
|---|---|---|
| Binance 永续 | 最新价 / 标记价 | 条件单（algo order），`clientAlgoId` 即 `clientOrderId` |
| Hyperliquid 永续 | 仅标记价 | trigger 单，触发后以触发价 ±10% 为保护价的市价单成交；含 builder dex（xyz: 等） |
| Lighter 永续 | 仅标记价 | 止损 / 止盈单，触发后 IOC，保护价同上；下单为异步交易，查询不到时返回 unknown 而不是 not_found |
| OKX 永续 | 最新价 / 标记价 | 单向条件单（`ordType=conditional`），按张数下单，`algoClOrdId` 即 `clientOrderId`；也列出在 OKX 页面设置的 OCO 止盈止损的市价腿 |
| Bybit 永续 | 最新价 / 标记价 | 条件市价单（`orderFilter=StopOrder`），按触发方向区分止盈 / 止损；`orderLinkId` 即 `clientOrderId` |
| Bitget 永续 | 最新价 / 标记价 | UTA 策略单（`type=tpsl`，部分止盈止损），单向模式只减仓、双向模式按持仓方向平仓 |
| Kraken 永续 | 最新价 / 标记价 | `stp` / `take_profit` 单，触发后市价成交 |
| Deribit 永续 | 最新价 / 标记价 | `stop_market` / `take_market` 单，`label` 即 `clientOrderId` |
| Gate 永续（原生账户） | 最新价 / 标记价 | 价格触发单（`price_orders`），触发后市价（IOC）只减仓；Gate 不接受触发单的 clientOrderId，以 `initial.text` 携带，查询时在挂单和最近 100 条已完成单中匹配 |

不支持：Gate CrossEx（只有市价 / 限价平仓）。

查询找不到时，Binance、OKX、Bybit、Hyperliquid 返回 `not_found`（交易所查询是权威的）；Lighter、Bitget、Kraken、Deribit、Gate 只能看到挂单和最近一段已完成单，返回错误（结果未知），调用方应稍后重查，不得重发。

#### POST /triggers

```json
{"inst":"binance:perp:ETH-USDT","side":"sell","kind":"stop_loss","triggerPrice":"2300",
 "priceType":"mark","qty":"0.5","clientOrderId":"229736271958529"}
```

| 字段 | 说明 |
|---|---|
| `side` | 平仓方向：多头为 `sell`，空头为 `buy` |
| `kind` | `take_profit` / `stop_loss` |
| `priceType` | `last` / `mark`，须在该市场的 `priceTypes` 中 |

返回与下单相同的三种结果（`trigger` 代替 `order`），HTTP 状态码也相同。`unknown` 时必须先用 `clientOrderId` 查询，不得重发。

#### GET /triggers?inst=

挂着的止盈止损单：`[{inst, orderId, clientOrderId, side, kind, triggerPrice, priceType, qty, closePosition, status, createdAt}]`。`closePosition` 表示在交易所界面设置的"全部平仓"单，没有固定数量。省略 `inst` 时列出账户所有支持触发单的市场。

#### GET /triggers/lookup?inst=&clientOrderId=

返回 `{status:"found"|"not_found", trigger?}`。

#### DELETE /triggers/{orderId}?inst=

撤销一个止盈止损单。

### PUT /leverage

`{inst, leverage}` → `{inst, leverage}`（交易所确认的值）。Deribit 返回 `unsupported`。
