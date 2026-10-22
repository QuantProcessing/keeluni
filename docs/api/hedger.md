# hedger

基址 `/v1`，鉴权 `Authorization: Bearer <HEDGER_TOKEN>`（`/v1/health` 除外）。经 trader 访问时为 `/api/hedge/v1`。请求体中的未知字段会被拒绝。

## 接口一览

```
GET   /v1/health
GET   /v1/groups?days=7                 进行中的组 + 最近 N 天结束的组（含实时视图）
GET   /v1/groups/{id}                   一组的视图 + actions / orders / fills / events / funding
GET   /v1/match?inst=a&inst=b           两个标的如何匹配
POST  /v1/preview                       开仓预览
POST  /v1/groups                        开仓
PATCH /v1/groups/{id}                   修改标签与备注
POST  /v1/groups/{id}/close             平仓（按比例）
POST  /v1/groups/{id}/cancel            撤销执行中的限价动作
POST  /v1/groups/{id}/rehedge           补腿
POST  /v1/groups/{id}/trim              减腿
POST  /v1/groups/{id}/unwind            全平
GET   /v1/ws                            推送组列表
```

## GET /v1/health

数据库可用且 gateway 推送在线时 `{"ok":true}`；否则 503 `{"ok":false,"error":"…"}`。

## GET /v1/match?inst=&inst=

```json
{"match":"base","ratio":"0.001"}       // PEPE 对 1000PEPE
{"match":"notional","ratio":"0"}       // BTC 对 ETH：按名义价值匹配，开仓时确定
```

## POST /v1/preview

```json
{"legs":[{"accountId":"acc_cx","inst":"okx:perp:BTC-USDT-SWAP","side":"buy"},
         {"accountId":"acc_cx","inst":"hyperliquid:perp:BTC-USDC","side":"sell"}],
 "size":"3000","unit":"quote","mode":"market","makerLeg":0,"policy":"unwind"}
```

| 字段 | 说明 |
|---|---|
| `legs[].accountId` | 可为空：为空时按交易所规则估算 |
| `legs[].side` | 两条腿必须一多一空 |
| `size` + `unit` | `quote`：每条腿的金额；`base`：腿 1 的币数量 |
| `mode` | `market`（双边市价）/ `limit`（限价 + 对冲） |
| `makerLeg`、`limitPrice`、`tif` | 限价模式：挂单腿（0/1）、限价、`gtc` 或 `post_only`（只作用于挂单） |

返回：

```json
{"qty":["0.0362","0.0362"],"notional":["2994.11","2994.82"],"ratio":"1","match":"base","mismatch":"0.71",
 "price":["82710.0","82729.0"],
 "sweep":[{"filled":"0.0362","avgPrice":"82710.0","best":"82710.0","slippageBps":"0","short":false}, …],
 "spreadBps":"2.3","funding":[{…}, {…}],"netFundingApr":"0.0196",
 "legs":[{"accountId":"acc_cx","broker":true,"stepSize":"0.0001","tickSize":"0.1","minQty":"0.0001","minNotional":"0", …}, …],
 "sharedPool":true,
 "problems":[{"code":"sizing.belowMinNotional","params":{"leg":"2","value":"4.98","min":"5","quote":"USDT"},"message":"…"}]}
```

- `sweep`：按当前盘口吃单的预计均价与滑点；`short` 表示可见盘口不足以成交该数量。
- `netFundingApr`：按两条腿当前资金费率算出的净年化。永续腿的资金费率还没收到时（hedger 首次用到某标的才订阅）**省略该字段**，表示未知而不是 0。
- `legs`：解析后的两条腿，带各自账户的路由规则。
- `sharedPool`：两条腿用同一个经纪账户。
- `problems`：编码文本列表，非空时开仓会被拒绝。

## POST /v1/groups

请求体同 preview，另加 `policy`（`unwind` / `hold`）、`label`、`note`；两条腿的账户必填。返回创建的组。

## POST /v1/groups/{id}/close

```json
{"fraction":"0.5","mode":"limit","makerLeg":0,"limitPrice":"82800","tif":"post_only"}
```

`fraction` 在 (0, 1] 之间。

## GET /v1/groups 与组视图

每个组：

| 字段 | 说明 |
|---|---|
| `id` `label` `note` `status` `policy` `match` `ratio` | 基本信息；`status` 见[状态](/hedger/#状态) |
| `problem` | 编码文本：为什么未对冲或失败 |
| `legs` | 两条腿的定义（账户、是否经纪账户、标的、方向、规则） |
| `working` | 执行中的动作（kind、mode、目标、状态） |
| `legView` | 每条腿的实时视图：持有、均价、标记价、名义价值、未实现/已实现盈亏、手续费、资金费率与下次结算、已收资金费、强平价与距离、杠杆、交易所持仓、账户推送是否在线 |
| `exposure` `exposureValue` `balanced` | 剩余方向敞口 |
| `entrySpreadBps` `spreadBps` `exitSpreadBps` | 入场 / 当前 / 出场价差 |
| `netFundingApr` `fundingEarned` `unrealizedPnl` `realizedPnl` `fees` `netPnl` `notional` | 汇总 |

`GET /v1/groups/{id}` 另返回 `actions`、`orders`（含每笔的 `error` 编码文本）、`fills`、`events`（`{id, at, level, message}`，`message` 为编码文本）、`funding`。

## WebSocket /v1/ws

推送 `{"ch":"groups","data":[…组视图…]}`：有变化时推送（最多每 250ms 一次），有活跃组时每 2 秒推送一次。

## 编码文本

hedger 发给用户的消息（日志、组的问题、动作与订单错误、开仓预检问题、请求被拒）都是**编码文本**：

```json
{"code":"failure.hedgeRejected","params":{"leg":"2"},
 "message":"hedge on leg 2 rejected: Insufficient margin (-2019)",
 "reason":{"message":"Insufficient margin (-2019)"}}
```

| 字段 | 说明 |
|---|---|
| `code` | 消息编码（`pairexec/text.go` 中的全部编码） |
| `params` | 参数；值为字符串 |
| `message` | 英文渲染：给日志、不认识该编码的客户端、以及翻译缺失时的兜底 |
| `reason` | 可选：原因，本身也是编码文本，或交易所原文（无 `code`） |
| `also` | 可选：后续的另一条文本（如两条腿各自的成交不足） |

没有 `code` 的文本是交易所或 gateway 的原文，在任何语言下都原样显示。改造之前写入数据库的纯字符串照常显示。

## 错误

被拒的请求（400）：

```json
{"error":{"code":"INVALID_REQUEST","message":"leg 1: account cx cannot trade okx:perp:BTC-USDT-SWAP",
          "text":{"code":"invalid.legRoute","params":{"leg":"1","account":"cx","inst":"okx:perp:BTC-USDT-SWAP"},"message":"…"}}}
```

其他：`NOT_FOUND`（404）、`GATEWAY`（502，gateway 返回的错误）、`INTERNAL`（500）、`INVALID_JSON`（400）。
