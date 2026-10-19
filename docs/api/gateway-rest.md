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

### GET /v1/funding

| 参数 | 返回 |
|---|---|
| `?inst=` | 单个标的的资金费率 |
| `?venue=&base=` | 资金费率表（可按交易所、底层币筛选）+ 各市场采集状态 |

```json
{"inst":"okx:perp:BTC-USDT-SWAP","venue":"okx","base":"BTC","rate":"0.0000408","intervalHours":8,
 "apr":"0.0447","nextFunding":1792396800000,"observedAt":1792392071143,"stale":false}
```

字段含义见[资金费率](/architecture/funding)。

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

### PUT /leverage

`{inst, leverage}` → `{inst, leverage}`（交易所确认的值）。Deribit 返回 `unsupported`。
