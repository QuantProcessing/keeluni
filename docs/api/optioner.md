# optioner

trader 在 `/api/options/*` 转发到 optioner 的 `/v1/*`，WebSocket 为 `/api/options/ws`。金额为美元（行权价币种），希腊值单位见[产品类别](/architecture/products#单位约定所有交易所一致)。

### GET /v1/risk?accounts=&hedges=1&days=0

`accounts` 逗号分隔（空为全部）；`hedges=0` 不计入永续 / 现货；`days` 为情景网格向后推移的天数。

```json
{
  "underlyings": [{
    "underlying": "BTC", "spot": 81500,
    "greeks": {"delta": 0.235, "deltaUsd": 19179, "gamma1pct": -2978, "vega": -141, "theta": 83, "value": -45957, "pnl": -2077},
    "hedgeDelta": -0.4,
    "expiries": [{"expiry": 1795766400000, "dte": 38.3, "greeks": {…}}],
    "positions": [{"account": "acc_d", "inst": "deribit:option:BTC-USD-20261127-80000-C", "product": "option", "kind": "call",
                   "strike": "80000", "expiry": 1795766400000, "size": -2, "entry": "0.06", "mark": "0.0704",
                   "markIv": 0.45, "premiumAsset": "BTC", "greeks": {…}}],
    "scenario": {"spotMoves": [-0.3, …, 0.3], "volMoves": [-20, …, 20], "pnl": [[…]], "days": 0}
  }],
  "updatedAt": 1792497489203,
  "stale": false
}
```

`stale` 为真：某个期权链或账户流不是最新的，或某个持仓不在期权链中。

### POST /v1/preview

请求体同 `POST /v1/executions`，返回解析后的各腿（价格、`pricedAt`），**不下单**。

### POST /v1/executions

```json
{"label": "Short straddle", "legs": [
  {"account": "acc_d", "inst": "deribit:option:BTC-USD-20261127-80000-C", "side": "sell", "qty": "2", "priceBy": "mid", "tif": "post_only"},
  {"account": "acc_d", "inst": "deribit:option:BTC-USD-20261127-80000-P", "side": "sell", "qty": "2", "priceBy": "iv", "iv": 0.46},
  {"account": "acc_d", "inst": "deribit:perp:BTC-USDC", "side": "buy", "qty": "0.1", "type": "market"}
]}
```

| 字段 | 说明 |
|---|---|
| `price` | 明确的限价（权利金资产） |
| `priceBy` | `mark` / `mid`（默认）/ `bid` / `ask` / `iv` |
| `iv` | `priceBy=iv` 时的 IV 小数 |
| `type` | `limit`（默认）；`market` 只用于永续与现货 |
| `tif` | `gtc`（默认）/ `post_only` / `ioc` |

返回执行（见下）。某条腿被拒绝不影响其他腿。

### GET /v1/executions

最近一天的执行，最新在前：

```json
[{"id": "ex_…", "label": "…", "status": "partial", "problem": "…", "createdAt": 0, "updatedAt": 0,
  "legs": [{"account": "…", "inst": "…", "side": "sell", "qty": "2", "price": "0.0605", "type": "limit", "tif": "gtc",
            "pricedAt": "mid", "clientOrderId": "…", "orderId": "…", "state": "filled", "filled": "2", "avgPrice": "0.0605"}]}]
```

腿的 `state`：`pending` / `sent` / `open` / `partially_filled` / `filled` / `canceled` / `rejected` / `unknown`。

### POST /v1/executions/{id}/cancel

撤销该执行所有仍在挂单的腿。

### GET /v1/hedge?underlying=BTC&account=&hedges=1

```json
{"underlying": "BTC", "delta": 0.235, "residual": 0.0003,
 "leg": {"account": "acc_d", "inst": "deribit:perp:BTC-USDC", "side": "sell", "qty": "0.2347", "type": "market"}}
```

### WebSocket /v1/ws?accounts=&hedges=&days=

有变化时（最多每 250ms）以及每 2 秒推送：

```json
{"ch": "risk", "data": {…}}
{"ch": "executions", "data": [ … ]}
```
