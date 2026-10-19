# 交易所差异

本页集中记录各交易所在账户、身份、持仓模式、保证金模式、杠杆、订单上的差异。所有交易所都遵守同一组不变量：

- exchange 层**不会隐式切换**交易所的账户体系、持仓模式、保证金模式；没有公共的 `SetAccountMode` / `SetPositionMode` / `SetMarginMode`。
- 永续只暴露 `SetLeverage`，每个交易所说明它是保持现有保证金范围、使用固定范围，还是交给交易所后端。
- 不要把账户体系、逻辑账户、持仓模式、保证金模式、杠杆合成一个"账户模式"——它们是独立的维度。

## 总表

| 交易所 | 账户体系 | 逻辑账户身份 | 持仓模式 | 保证金模式 | 杠杆 | 订单注意事项 |
|---|---|---|---|---|---|---|
| Binance | 现货与 U 本位分开的 API | API key + secret | 永续单向（`BOTH`）；双向持仓的回包被拒绝 | 沿用账户现有全仓/逐仓 | `SetLeverage` 返回交易所确认值 | USDT 与 USDC 永续是同一个 U 本位账户；含 TradFi 永续 |
| OKX | 一个交易账户覆盖现货与 SWAP | key + secret + passphrase | 下单使用 `net` | 下单使用 `cross`；改杠杆保持已有的全仓/逐仓 | 校验返回的范围 | 多个同时存在的持仓范围不可移植 |
| Bybit | `UNIFIED` 统一账户 | key + secret | 不带对冲索引，即 `positionIdx=0` 单向 | 沿用账户现有模式 | 多空设置相同杠杆 | 现货市价买入把币数量换算为计价数量 |
| Bitget | `UTA` 账户 | key + secret + passphrase | 读取 `one_way_mode` / `hedge_mode` 并映射方向 | 永续使用 `crossed` | 使用该路由的结算币 | 对冲模式下的只减仓用持仓方向编码 |
| Gate | 现货识别经典/统一账户；永续使用合约账户 | key + secret | 单向 | 沿用合约账户模式 | 返回确认值 | 永续数量必须对齐合约面值（换算为整数张数） |
| Hyperliquid | 一个 owner 账户覆盖现货与永续 | 签名地址，或 `WithAccountAddress` 指定的 owner | 每个资产一个带符号的单向仓位 | 保持当前全仓/逐仓；无仓位时默认全仓 | 已知时校验标的最大杠杆 | API 钱包签名，可查询指定 owner；HIP-3 builder 永续带 dex 前缀 |
| Lighter | 一个带序号的账户 | 私钥 + `accountIndex` + `keyIndex` | 每个市场一个带符号的仓位 | 沿用账户/仓位的全仓或逐仓 | 复用已发现的保证金模式 | 交易所的市场与账户序号留在内部 |
| Kraken Futures | 多币种保证金（flex）账户；只支持线性 `PF_` 永续 | key + base64 secret | 每个合约一个净仓位 | 默认全仓；设置杠杆偏好会让该合约变为逐仓 | `SetLeverage` 设置该合约的逐仓最大杠杆 | 同步确认；撤单推送只带 id，私有流自己维护挂单；K 线轮询 REST |
| Deribit | 每个币种一个账户；只支持 USDC 线性永续 | client id + secret（client_credentials） | 每个标的一个净仓位 | 账户级全仓（或组合保证金） | 不支持，返回 `unsupported`：杠杆由仓位大小决定 | clientOrderId 以 label 传递，订单结束后 30 分钟内可查 |
| **Gate CrossEx**（经纪） | 一个跨所保证金池覆盖全部可达交易所；分所模式账户拒绝接入 | key + secret（CrossEx 权限） | 只支持单向（`SINGLE`）；双向账户拒绝 | 跨所资金池；Hyperliquid 仓位在交易所侧可能是逐仓 | 每个 CrossEx symbol 返回确认值 | 标的保持可达交易所的身份、使用 CrossEx 规则；确认为 `AckAcceptedPending`；行情方法返回 `unsupported` |

## 各交易所说明

### Binance

永续是单向的。带对冲方向的订单、历史或推送会被视为异常，而不是悄悄合并为一个仓位。`binance:perp:USDC` 与 `binance:perp:USDT` 是同一个 U 本位账户：gateway 只建一个客户端、一条私有流，余额都记在 `binance:perp:USDT` 名下；私有事件按标的过滤到各自的路由。TradFi 永续（股票、大宗等）出现在 USDT 永续中，带 `assetClass`。

### OKX

SWAP 下单使用全仓/net 的可移植形态。改杠杆前先查当前仓位；同一标的同时存在多个持仓范围时拒绝，因为一个标的级的请求无法可移植地指明其中一个。

### Bybit

账户读取使用统一钱包接口。规范化的下单请求没有对冲方向选择，调用方需要使用兼容的单向持仓。

### Bitget

下单前读取账户的持仓模式；单向与对冲账户都能映射，但下单与杠杆统一使用全仓。

### Gate

现货余额读取会识别经典与统一账户模式。永续使用 USDT 结算的合约接口，把规范化的币数量换算为整数张数。

### Hyperliquid

签名身份与查询的 owner 可以不同：显式的 owner 选项改变账户维度的 REST 与私有流读取，同时仍用签名者执行鉴权操作。资金费率每整点结算；HIP-3 dex 的标的（如 `xyz:TSLA`）与主 dex 一起由 `allDexsAssetCtxs` 推送。

### Lighter

配置的账户序号会与账户回包核对。改杠杆复用该仓位已报告的保证金模式，没有仓位时使用账户的交易模式。资金费率 REST 是 8 小时等效值，WebSocket 是百分数/小时。精确查单扫描挂单与最近 100 条历史订单。

### Kraken Futures

Kraken 把比特币写作 `XBT`（`PF_XBTUSD`），规范 symbol 使用标的的 `base`（`BTC-USD`）。合约面值为 1 个基础币，数量步长为 `10^-contractValueTradePrecision`。盘口增量带连续的 `seq`，断档时重连拿新快照。资金费率是 `relativeFundingRate`（每小时、按仓位价值的比例）。订单结束后约 5 秒内可查；之后精确查单搜索最近成交，否则 `unconfirmed`；历史订单由成交重建。`open_positions` 每条消息都是全量，缺少某个标的即表示该仓位已平。

### Deribit

线性永续的数量是基础币单位，步长为 `contract_size`。盘口变化以 `prev_change_id` 串联，断链时重新订阅。每小时资金费率 = `funding_8h / 8`（连续计息）。Deribit 没有 4 小时 K 线，4 小时 K 线由小时线聚合。

### Gate CrossEx

经纪型客户端，详见 [Gate CrossEx](./crossex)。

## 未服务的交易所

`exchange/` 还实现了 Aster（现货、USDT 永续）与 Nado（USDT0 现货与永续）：

- **Aster**：用户账户与独立的 API 钱包签名者；工厂校验可在本地验证预期签名地址；永续使用净仓位方向；WebSocket facet 的订单命令走签名 REST。
- **Nado**：子账户是逻辑身份的一部分；杠杆由后端管理，`SetLeverage` 返回 `Effective=0`；标记价订阅是有文档的方法级例外（`unsupported`）。

它们不在 gateway 的 `Markets` 中，因此不出现在终端里。
