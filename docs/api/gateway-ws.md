# gateway WebSocket

`GET /v1/ws`（经 trader 为 `/api/ws`）。单连接多订阅。

## 客户端 → 服务端

```json
{"op":"sub","ch":"book","inst":"binance:perp:BTC-USDT"}
{"op":"sub","ch":"trades","inst":"binance:perp:BTC-USDT"}
{"op":"sub","ch":"candle","inst":"binance:perp:BTC-USDT","interval":"1m"}
{"op":"sub","ch":"ticker","inst":"binance:perp:BTC-USDT"}
{"op":"sub","ch":"funding","inst":"okx:perp:BTC-USDT-SWAP"}
{"op":"sub","ch":"account","account":"acc_7f3k2m"}
{"op":"unsub", …同上}
{"op":"ping"}
```

## 服务端 → 客户端

```json
{"ch":"book","inst":"binance:perp:BTC-USDT","data":{…}}
{"ch":"account.positions","account":"acc_7f3k2m","data":[…]}
```

| ch | data | 说明 |
|---|---|---|
| `book` | `{bids:[[p,q]…], asks:[…], ts}` | gateway 维护本地订单簿，推送 top-N **完整快照**，100ms 节流 |
| `trades` | `[{p, q, side, ts}]` | 逐笔成交 |
| `candle` | `{t, o, h, l, c, v}` | 当前 K 线 |
| `ticker` | `{mark, index, funding, fundingIntervalHours, fundingApr, nextFunding, oi, ts}` | 资金费率字段来自常驻数据集 |
| `funding` | 资金费率记录（同 `GET /v1/funding?inst=`） | 订阅时先推当前值，之后有变化才推；不开新的交易所连接 |
| `account.orders` | 单个订单的最新完整状态（可含 `rejectCode`、`rejectMessage`、`origin`） | 按 orderId 更新 |
| `account.fills` | 单笔成交 | |
| `account.positions` | 该账户**全部持仓的完整列表** | 直接替换，不需要合并 |
| `account.balances` | 该账户全部余额的完整列表 | 直接替换 |
| `account.status` | `{connected, reason}` | 私有流状态 |
| `account.margin` | 同 `GET .../margin` | 仅经纪账户；完整快照 |

订阅 `account` 一个频道即收到上表全部 `account.*` 消息。

## 行为

- **共享上游**：同一 `(ch, inst)` 在 gateway 内只开一个上游订阅，引用计数；最后一个订阅者离开后延迟 30 秒关闭。
- **保留最后一条**：盘口、ticker、资金费率、持仓、余额、状态、保证金等保留型频道，新订阅者立即收到当前值。
- **慢消费者**：跟不上的连接会丢消息而不是阻塞上游；保留型频道的下一条完整消息即可修复。
- **重连**：客户端断线后应重新订阅；保留型频道会立即补发当前值。
