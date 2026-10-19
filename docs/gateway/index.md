# gateway

gateway 是 KeelUni 与交易所之间的唯一通道。它把 exchange 层的契约暴露为 HTTP 与 WebSocket，持有凭证、连接与限流，维护可从交易所重建的当前事实。职责边界见[总览与分层](/architecture/#gateway-是什么)。

## 包结构

| 包 | 职责 |
|---|---|
| `venue` | 服务哪些市场（`Markets`）；instrument id 的构造与解析；哪些是经纪账户（`Brokers`） |
| `market` | 标的目录、REST 行情（盘口、K 线、ticker）、公共 WebSocket 源；不接触凭证 |
| `account` | 账户的注册、客户端、私有流、持仓/余额快照、下单命令；不保存订单账本 |
| `hub` | 一个上游 → 多个 WebSocket 客户端的扇出；引用计数、延迟关闭、保留最后一条 |
| `resident` | 常驻数据集的后台循环与状态 |
| `funding` | 资金费率数据集（见[资金费率](/architecture/funding)） |
| `ratelimit` | 按交易所、按账户的请求预算（见[限流与连接](/architecture/rate-limits)） |
| `store` | `accounts.enc` 的加密读写 |
| `api` | `/v1` 的 HTTP 与 WebSocket 处理 |

## 标的目录（market.Catalog）

- 每个市场一个公共连接器，提供该市场的标的与规则（tick、step、最小数量、最小金额、资产类别）。
- 启动时所有市场并发加载；之后每 15 分钟刷新；尚未加载成功的市场每 30 秒重试。
- 某个市场刷新失败时**保留上一份成功的快照**；状态（何时更新、错误）见 `/v1/health` 的 `markets`。
- `GET /v1/instruments?venue=&product=` 列出目录；未知 id 返回 `UNKNOWN_INSTRUMENT`。

## 公共行情（market.Feeds）

| 频道 | 来源与处理 |
|---|---|
| `book` | 交易所盘口增量 → 本地订单簿；推送 top-N **完整快照**，100ms 节流；序列号断档时重新同步 |
| `trades` | 逐笔成交 |
| `candle` | K 线（Kraken 每 5 秒轮询 REST；Deribit 4 小时线由小时线聚合） |
| `ticker` | 标记价、指数价、持仓量来自交易所的衍生品流；资金费率字段来自常驻数据集 |

`GET /v1/ticker` 的 24 小时统计由小时 K 线派生。

## 账户（account.Manager）

### 创建

`POST /v1/accounts {venue, label, credentials}`：

1. 按 `/v1/venues` 给出的字段表把凭证分为元数据与密钥两部分；
2. 构造客户端（本地，无网络）；
3. **在该账户的每个市场上查询一次余额**来验证凭证；CrossEx 额外要求跨所模式；
4. 加密写入 `accounts.enc`；
5. 启动私有流。

失败按 category 返回；`network` 表示连不上交易所，不代表密钥有误。

### 运行时（runtime）

每个账户一个 runtime：一组客户端（link）与内存中的持仓/余额快照。

- **快照按客户端分区**：原生账户每个市场一份；经纪账户全部市场一份。
- **持仓**：订阅开始/重连时 REST 全量；WebSocket 增量（快照事件替换该标的的全部行，增量事件只替换出现的仓位槽）；成交、下单成功、私有流重连后防抖 300ms 再拉 REST；每 60 秒全量校正。
- **余额**：每次读取都直接查交易所（各市场并发）。
- **保证金池**：只有经纪账户有，`account.margin` 推送完整快照。
- **私有事件过滤**：
  - Binance U 本位一个客户端交易 USDT 与 USDC 两个市场，事件按标的过滤到各自路由；
  - Kraken、Deribit 只转发 gateway 服务的合约（例如 Deribit 的期权、Kraken 的反向合约不转发）。
- **clientFor(instrument)**：找出路由包含该标的的客户端；没有则 `invalid`（该账户不能交易此标的）。

### 下单

`Place` 只做三件事：用路由规则校验、调用交易所、把确认翻译为 accepted / rejected / unknown。**不重试、不落库**。语义见[订单生命周期](/architecture/orders)。

## hub

- 一个 topic（频道 + 标的/账户/周期）在第一个订阅者到来时打开上游，最后一个离开后延迟 30 秒关闭。
- 保留型频道（盘口快照、持仓、余额、状态、保证金、资金费率）保留最新一条，后来的订阅者立即收到。
- 跟不上的订阅者丢消息而不阻塞上游；保留型频道的下一条完整消息即可修复。
- 三个 hub：公共行情、账户、数据集。

## 存储

gateway 唯一的持久化：

| 文件 | 内容 |
|---|---|
| `accounts.enc` | JSON 记录数组；每条凭证 AES-256-GCM 单独加密，附加数据绑定「账户 id + 交易所」；原子写；0600 |
| `master.key` | 主密钥，首次启动生成，0600 |
| `token` | 调用方使用的 bearer token，首次启动生成，0600 |

其余一切（目录、快照、数据集）都在内存中，重启后从交易所重建。

## gwclient

`gwclient/` 是给应用（如 hedger）用的 Go 客户端，覆盖 `/v1` 的 REST 与 WebSocket：`Instruments`、`Accounts`（含 `Kind`、`Markets`）、`Routes`、`Margin`、`OpenOrders`、`Place`、`Lookup`（`LookupFound` / `LookupNotFound` / `LookupUnconfirmed`）、`Cancel`、`Fills`、`FundingHistory`，以及 `NewClientOrderID`。所有数值与线上格式一致，为十进制字符串。
