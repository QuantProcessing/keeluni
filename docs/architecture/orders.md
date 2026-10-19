# 订单生命周期

下单是整个系统里最需要正确的部分。核心原则只有一条：**交易所是唯一事实来源，结果不明时只查不重下。**

## 三种结果

调用方生成 clientOrderId，`POST /v1/accounts/{id}/orders`。gateway 直接调用交易所，**不重试、不落库**，返回三种结果之一：

| 结果 | HTTP | 含义 |
|---|---|---|
| `accepted` | 200 | 交易所已接受，附订单当前状态（市价单可能已经 `filled`） |
| `rejected` | 422 | 交易所明确拒绝，附 `venueCode` / `venueMessage`（交易所原文） |
| `unknown` | 202 | 超时、断连、回包无法解析：订单可能下出，也可能没有 |

三种结果的响应体都是 `PlaceResult`。

## 结果未知时怎么办

`unknown` 时调用方：

- **禁止**用新的 id 重下（可能重复下单）；
- **禁止**盲目用同一个 id 重下（各交易所对重复 clientOrderId 的去重范围不同，有的只在挂单中去重）；
- **必须**调用 `GET /v1/accounts/{id}/orders/lookup?inst=&clientOrderId=` 确认。

查询结果：

| status | 含义 | 调用方怎么做 |
|---|---|---|
| `found` | 找到订单，附当前状态 | 按订单状态继续 |
| `not_found` | 交易所能证明没有这笔订单 | 订单从未下出；hedger 只在下单 10 秒后才采信 |
| `unconfirmed` | 交易所既不能显示也不能否认 | 用其他证据（成交、当前挂单）确认；见下 |

### 为什么会有 `unconfirmed`

有些交易所会「忘记」已结束的订单：

| 交易所 | 行为 | 查单实现 |
|---|---|---|
| Kraken Futures | 订单结束后约 5 秒内可查 | 先查订单状态，再查最近成交，否则 `unconfirmed` |
| Deribit | client order id 以 label 传递，订单结束后 30 分钟内可按 label 查 | 超时后 `unconfirmed` |
| Gate CrossEx | 文档注明已结束订单可能无法再用 text 查询 | 查不到时扫描历史订单，仍不能证明则 `unconfirmed` |
| Lighter | 扫描挂单与最近 100 条历史订单 | 频繁下单时极早的订单可能 `unconfirmed` |

这三家（Kraken、Deribit、CrossEx）刻意返回 `unconfirmed` 而不是 `not_found`：没有证据时不能声称订单不存在。

前端遇到 `unconfirmed` 时提示「结果未知，重试前请先检查当前委托」。hedger 会用该订单的成交记录和当前挂单来确认：有成交就按成交定，在挂单里就继续跟踪；2 分钟内都找不到，订单记为 `unresolved`，对冲组标记为未对冲，并**跳过自动修复**。

## 异步确认（经纪账户）

经纪账户（CrossEx）的 `accepted` 只表示经纪商已受理：

```
POST /orders ──> accepted, order.status = "pending"
                     │
       account.orders 推送
                     ├─> open / partially_filled / filled …   （交易所已接受）
                     └─> rejected, rejectCode, rejectMessage （之后被拒）
```

- `rejectCode`：经纪商风控标签（如 `TRADE_INSUFFICIENT_AVAILABLE_MARGIN_ERROR`）。
- `rejectMessage`：交易所原文。

这不是 CrossEx 专用分支：所有返回 `AckAcceptedPending` 的交易所都适用同一处理。前端对本终端下的订单记住 10 分钟，期间收到 `rejected` 会弹出提示；hedger 把异步拒单与同步拒单同样对待（对冲单被拒立即停止动作，不会再补发）。

## 风控订单

交易所风控引擎下的订单带 `origin`：

| origin | 含义 |
|---|---|
| `liquidation` | 强平 |
| `adl` | 自动减仓 |
| `settlement` | 下架结算 |

前端在委托与持仓中单独标出并弹出提示；hedger 把它记到涉及该账户与标的的每个活跃对冲组的日志中，解释为什么交易所持仓变了。

## 之后的状态变化

订单之后的变化全部经私有流推送：

| 频道 | 内容 |
|---|---|
| `account.orders` | 单个订单的最新完整状态（按 orderId 更新） |
| `account.fills` | 单笔成交 |
| `account.positions` | 该账户全部持仓的完整列表 |
| `account.balances` | 该账户全部余额的完整列表 |

### 持仓快照

gateway 为每个账户维护内存持仓表：

- 订阅开始、重连时用 REST 拉全量；
- WebSocket 增量更新，每次变化推送完整列表；
- 收到成交、下单成功或私有流重连后，防抖 300ms 再拉一次 REST（兜底那些不推送持仓的交易所）；
- 每 60 秒全量校正一次。

`GET .../positions` 在私有流在线时返回内存快照，否则实时读交易所。

## 杠杆

杠杆不随下单设置。只有用户修改时调用 `PUT /v1/accounts/{id}/leverage {inst, leverage}`，返回交易所确认的值。

| 交易所 | 行为 |
|---|---|
| 多数 | 设置该标的的杠杆，返回确认值 |
| Kraken Futures | 设置杠杆偏好会让该合约变为逐仓 |
| Deribit | 不支持（杠杆由仓位大小决定），返回 `unsupported` |
| Nado | 杠杆由后端管理，返回 `Effective=0` |
