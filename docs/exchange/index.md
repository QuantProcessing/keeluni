# sdk 与 exchange

## sdk：忠实的原生实现

`sdk/` 为每个交易所实现原生 REST 与 WebSocket，目标是**忠实**：签名、端点、原生类型与交易所文档一致，不做跨交易所的统一。

| 包 | 内容 |
|---|---|
| `sdk/binance` `okx` `bybit` `bitget` `gate` `hyperliquid` `lighter` | 现货与永续 |
| `sdk/kraken` | Kraken Futures：REST（签名用官方测试向量验证）；WebSocket `Stream.Run`，私有频道通过 challenge 鉴权 |
| `sdk/deribit` | JSON-RPC over GET，client_credentials 令牌（过期前 1 分钟刷新，遇错误 13009 丢弃）；WebSocket 心跳以 `public/test` 应答，在连接上 `public/auth` |
| `sdk/crossex` | Gate CrossEx：签名同 Gate APIv4；私有 WebSocket `login` 事件签名；手写客户端，不引入生成的 `gateapi-go` |
| `sdk/aster` `nado` `backpack` `edgex` `grvt` | 已实现，gateway 当前未服务 |

每个 sdk 的 `testdata/` 保存实际抓取的回包，测试以它们为 fixture。

## exchange：规范化契约

`exchange/` 把各交易所规范化为**一套带类型的客户端契约**。应用只依赖这一层，不接触交易所的原生载荷。

### 构造

每个「交易所 × 产品」有一个构造函数，返回密封的 `factory.Config[C]`；`factory.New` 校验后返回 `exchange.SpotClient` 或 `exchange.PerpClient`：

```go
client, err := factory.New(factory.OKXUSDTPerpConfig(key, secret, passphrase))
// client 的类型是 exchange.PerpClient：编译期就知道它有 Positions、SetLeverage
```

- 构造在本地完成：校验凭证、环境、端点选项，组装 sdk 客户端，**不做网络 I/O**。第一次 REST 调用或 WebSocket 订阅才真正连接交易所。
- 只有一个环境：`factory.EnvironmentLive`（默认）。演示/测试网环境已移除，因为各交易所实现差异太大。
- 匿名访问只读，使用实盘公共数据。

主要构造函数：`BinanceSpotConfig`、`BinanceUSDPerpConfig`、`BinanceUSDCPerpConfig`、`OKXSpotConfig`、`OKXUSDTPerpConfig`、`BybitSpotConfig`、`BybitUSDTPerpConfig`、`BybitUSDCPerpConfig`、`BitgetSpotConfig`、`BitgetUSDTPerpConfig`、`BitgetUSDCPerpConfig`、`GateSpotConfig`、`GateUSDTPerpConfig`、`HyperliquidSpotConfig`、`HyperliquidPerpConfig`、`LighterSpotConfig`、`LighterPerpConfig`、`KrakenPerpConfig`、`DeribitPerpConfig`、`CrossExPerpConfig`。

### 按产品划分的接口

现货与永续的方法集在编译期分开，不做运行时的能力探测：

| 接口 | 现货 | 永续 | 内容 |
|---|---|---|---|
| `MarketREST` | ✓ | ✓ | 标的、盘口、K 线、公开成交 |
| `OrderREST` | ✓ | ✓ | 下单、撤单、挂单、历史订单、成交 |
| `SpotAccountREST` | ✓ | | 余额、现货账户 |
| `PerpAccountREST` | | ✓ | 余额、合约账户、持仓 |
| `PerpREST` | | ✓ | 资金费率、杠杆 |
| `SpotWebSocket` | 返回 | 被 Perp 嵌入 | 公共/私有流与订单命令 |
| `PerpWebSocket` | | 返回 | 加上持仓与永续参考价流 |

`SpotClient` 因此无法调用持仓、资金费率、杠杆方法。个别交易所在某个方法上不支持时，该方法返回 `exchange.ErrUnsupported`，而不是从接口中删除。

### 规范化的数据

- 标的在公共边界使用规范 symbol；交易所的市场 id、币种序号、张数、缩放整数都留在 sdk 翻译层内部。
- 所有网络方法接受 `context.Context`。
- 价格、数量、余额、手续费、资金费率等精确数值都用 `decimal.Decimal` 或显式的可选 decimal 包装，调用方从不解析原生 JSON。
- 分页只保留交易所能证明的事实：没有游标或 `HasMore` 未知，不代表历史已经完整。

### 订单

REST 与 WebSocket 的订单命令共用 `exchange.PlaceOrderRequest`：

- 市价单有数量、没有限价；
- 限价单需要正的价格，以及挂单 / IOC / 只做 Maker 之一；
- `ReduceOnly` 只对永续有效；
- `ClientOrderID` 必填，十进制 uint48 字符串；
- 撤单使用该订单返回的不透明 `OrderID`。

命令返回 `exchange.OrderAcknowledgement`，**只陈述交易所回包能证明的状态**，不猜测最终结果：

| 确认状态 | 含义 |
|---|---|
| `accepted_pending` | 已受理，尚未确认（经纪账户） |
| `resting` | 已挂单 |
| `partially_filled` | 部分成交 |
| `immediately_filled` | 立即全部成交 |
| `canceled` | 已撤销（如 IOC 余量） |
| `rejected` | 被拒 |
| `ambiguous` | 无法确定；必须先按原始标识核对，才能再写入 |

`exchange.ExactOrderLookup` 按 clientOrderId 或 orderId 精确查单，返回 `found` / `not_found` / `unconfirmed`（见[订单生命周期](/architecture/orders)）。

### WebSocket

`WebSocket()` 返回一个延迟连接的带类型 facet。每个订阅返回 `exchange.Subscription[T]`：不透明的 ID，加上 `Events`、`Status`、`Errors` 与幂等的 `Close`。启动失败从订阅调用返回；之后的异步错误从 `Errors` 送达。状态事件区分 connecting、active、gap、resyncing、closed。

盘口增量依赖序列号：Kraken 的 `seq` 断档、Deribit 的 `prev_change_id` 断链都会触发重新同步。

### 错误

所有错误都是 `exchange.Error`，按 kind 分类，细节安全（不含凭证、签名、原生载荷）：

| kind | 含义 |
|---|---|
| `invalid_config` / `invalid_request` | 构造参数或请求本身不合法 |
| `authentication` / `permission` | 密钥、签名或权限被拒 |
| `rate_limit` | 被限流 |
| `not_found` | 不存在 |
| `unsupported` | 交易所/市场不支持该操作 |
| `venue_rejected` | 交易所收到并拒绝 |
| `transport` | 网络失败 |
| `ambiguous_outcome` | 写操作后网络失败，结果未知——**禁止据此盲目重试** |
| `malformed_response` / `metadata_resolution` | 回包无法解析 / 标的元数据无法解析 |
| `canceled` / `deadline_exceeded` | 上下文取消或超时 |
| `subscription_gap` / `subscription_closed` | 订阅断档或关闭 |

gateway 再把这些 kind 映射为面向调用方的 7 个 category（见 [API 概览](/api/#错误)）。

### 免凭证的公共读取

- `factory.NewPublicDerivativeFeed`：一个交易所市场族的衍生品事实（资金费率、标记价、指数价、持仓量），可同时投影到 USDT 与 USDC 路由，不重复打开原生数据源。
- `factory.NewPublicFundingHistoryConnector`：单个标的在给定窗口内的已结算资金费率。
- `factory.PublicFundingBoard`：全市场资金费率板，gateway 资金费率数据集的来源。

快照与增量在进入事实存储前都会校验身份，适配器不能悄悄发布另一个交易所或路由的数据。不提供历史的交易所返回 `exchange.ErrUnsupported`，让「没有」成为一个有文档的答案，而不是空序列。

### 经纪型客户端

Gate CrossEx 的客户端额外实现 `exchange.BrokeredClient`（调用方用类型断言发现），是「客户端只发布自己交易所的标的」这一不变量的唯一例外。详见 [Gate CrossEx](./crossex)。

## 公开 API 清单

`exchange/testdata/public_surface_manifest.json` 记录 `exchange/` 的每个导出符号，`TestPublicSurfaceManifestCoversExportedSymbols` 保证二者一致。新增导出符号时必须在清单中**追加**条目（只追加；可以原样删除失效的条目）。
