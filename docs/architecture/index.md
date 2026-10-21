# 总览与分层

KeelUni 由六层组成，依赖只能自上而下：

```
web        React 终端（交易 + 期权 + 对冲）
  │  同源 cookie 会话
trader     登录、会话、反向代理、提供前端                — 无数据库
  │  bearer token，仅内网
hedger     对冲应用                                      — 自己的 PostgreSQL
optioner   期权应用                                      — 无数据库（交易所即事实）
  │  bearer token
gateway    与交易所无关的 REST + WebSocket API          — 只有一个加密文件
  │
exchange   规范化的客户端契约、模型、错误
  │
sdk        各交易所原生 API，忠实实现
```

| 层 | 职责 | 不做什么 |
|---|---|---|
| `sdk/` | 忠实实现每个交易所的原生 REST / WebSocket：签名、请求、原生类型 | 不做跨交易所的统一 |
| `exchange/` | 把各交易所规范化为一套带类型的客户端契约：标识、单位、规则、订单确认、错误分类 | 不做网络服务、不保存状态 |
| `gateway/` | 把契约暴露为 HTTP / WebSocket；持有凭证、连接、限流；维护标的目录、持仓快照与常驻数据集 | 不做业务、不保存订单账本、不需要数据库 |
| `trader/` | 单人登录、会话、把浏览器请求转发到 gateway / hedger、提供前端静态文件 | 无数据库、不直接访问交易所 |
| `pairexec/` | 两腿执行内核：组、动作、订单、成交；双边市价 / 一腿挂单一腿对冲、单腿失败回退、结果未知查询不重发、与交易所对账；PostgreSQL 存储（每个应用一个表前缀） | 不做业务决策 |
| `hedger/` | 对冲业务（资金费率套利）：在 `pairexec` 上提供开仓预览与 API | 不直接访问交易所 |
| `optioner/` | 期权业务：组合风险（含币本位权利金 Delta、情景网格）、多腿执行、Delta 对冲 | 不直接访问交易所、不存储 |
| `optionmath/` | Black-76 定价、希腊值、隐含波动率；gateway 与 optioner 共用 | 纯计算 |
| `web/` | 终端界面 | 不持有任何凭证 |

## gateway 是什么

判断一件事放在 gateway 还是应用里，以下面三条为准：

1. **交易所的唯一出口。** 所有访问交易所的流量（公开或私有、REST 或 WebSocket）都经过 gateway。限流、连接、代理、凭证只存在于 gateway，所以同一交易所的额度可以统一分配并分优先级：下单/撤单 > 用户触发的查询 > 后台常驻采集（最多占该交易所额度的 25%）。应用从不直连交易所。
2. **只提供交易所的当前事实。** 可以在内存中常驻持有标的目录、持仓快照、资金费率等全市场数据集，但必须能在重启后从交易所重新拉回。不积累交易所给不回来的历史，不保存订单账本（幂等靠交易所的 clientOrderId，交易所是事实来源），不依赖数据库：应用的数据库故障时交易照常。
3. **做规范化，不做业务。** 统一各交易所的标识、单位与语义（例如资金费率统一为"单次结算比例 + 结算周期 + 年化"）。凡是涉及两个标的或交易所之间的关系、任何策略、多笔订单的编排，都属于应用。

因此常驻采集按「是否需要积累」划分：交易所的当前全市场事实是 gateway 的常驻数据集；超出交易所保留期的时间序列、跨标的的计算，放在拥有自己数据库的应用里。对冲就是这样一个应用。

## 一次下单经过哪些层

```
web          生成 clientOrderId，POST /api/gw/v1/accounts/{id}/orders
trader       校验会话、Origin、X-KeelUni；换成 gateway token 转发
gateway      找到能交易该标的的账户 link；按路由规则校验；限流（下单优先）
exchange     PlaceOrderRequest → 交易所原生请求；回包 → OrderAcknowledgement
sdk          签名、发送
gateway      accepted / rejected / unknown 三种结果之一，原样返回
web          unknown 时按 clientOrderId 查询，绝不重下
gateway      之后的状态变化经私有流推送 account.orders / account.fills / account.positions
```

详见[订单生命周期](./orders)。

## 一条行情经过哪些层

```
web          WebSocket 订阅 {"op":"sub","ch":"book","inst":"okx:perp:BTC-USDT-SWAP"}
trader       透传到 gateway /v1/ws
gateway hub  同一 (频道, 标的) 只开一个上游订阅，引用计数；最后一个订阅者离开后延迟 30 秒关闭
gateway      本地维护订单簿，按 100ms 节流推送 top-N 全量快照
exchange     交易所原生增量 → 规范化事件；序列号断档时重新同步
```

行情永远来自订单簿所在交易所的原生接口，与用哪个账户交易无关。

## 进程启动顺序（gateway）

1. 读取主密钥与 token（首次启动生成）。
2. 加载标的目录：所有市场并发加载；之后每 15 分钟刷新，尚未加载成功的市场每 30 秒重试；某个市场刷新失败时保留上一份快照。
3. 启动账户管理器：解密 `accounts.enc`，为每个账户建立客户端与私有流。
4. 启动常驻数据集（资金费率、期权链）。
5. 建立公共、私有、数据集三个 hub，并为每个市场预热一次盘口订阅，避免首个用户承担 TLS 握手。
6. 开始监听 `/v1`。

## 代码目录

```
sdk/            各交易所原生 API（binance、okx、bybit、bitget、gate、hyperliquid、lighter、kraken、deribit、crossex…）
exchange/       规范化契约：contract.go、model.go、ack.go、errors.go、broker.go
  factory/      带类型的构造（XxxConfig → Client）、公共行情/资金费率连接器
  internal/factoryclient/  契约到各 sdk 的翻译
routeprofile/   每个交易所的产品/结算路由表
credential/     各交易所凭证字段的定义与校验
secretstore/    凭证加密（AES-256-GCM）
gateway/
  venue/        服务哪些市场；instrument id 的解析
  market/       标的目录、REST 行情、公共 WebSocket 源
  account/      账户生命周期、私有流、持仓快照、下单
  hub/          一个上游 → 多个 WebSocket 客户端的扇出
  resident/     常驻数据集的后台循环
  funding/      资金费率数据集
  optionchain/  期权链数据集（按系列：标的 × 权利金资产）
  ratelimit/    按交易所的请求预算
  store/        accounts.enc 的读写
  api/          /v1 HTTP 与 WebSocket
gwclient/       gateway API 的 Go 客户端（给应用用）
trader/         登录、会话、反向代理、静态文件
pairexec/       两腿执行内核（hedger、spreader 共用）
hedger/         对冲应用
optioner/       期权应用
optionmath/     期权定价
web/            前端
cmd/            gateway、trader、hedger、optioner、smoke 五个可执行程序
```
