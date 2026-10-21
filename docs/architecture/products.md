# 产品类别

KeelUni 的每一层都以 **Product** 区分标的的经济形态。现在有三种，大宗商品是下一种：

| Product | 持有的是 | 数量单位 | 价格单位 | 例子 |
|---|---|---|---|---|
| `spot` | 余额 | 基础币 | 计价币 | `kraken:spot:BTC-USD` |
| `perp` | 带符号的净仓位 | 基础币 | 计价币 | `deribit:perp:BTC-USDC` |
| `option` | 带符号的净仓位 | **标的数量**（1 = 一个 BTC 的期权） | **权利金资产**每单位标的 | `deribit:option:BTC-USD-20261127-73000-P` |

## 加一个产品要动哪里

产品集合是开放的：每一层对 `Product` 分支，不认识的产品一律视为不支持，所以新增一种是**纯增量**。期权就是按下面的顺序接入的，大宗商品照此办理：

| 层 | 改动 |
|---|---|
| `routeprofile` | 新的 `{venue, product, settlement}` 行 |
| `exchange` | `Product` 常量、`KnownProduct` / `IsDerivative`；产品专属条款（期权的 `OptionTerms`）；产品的客户端契约（`OptionClient`）与可选 facet |
| `exchange/internal/factoryclient` | 适配器；尽量复用同一交易所其他产品的实现（Deribit 的永续、现货、期权共享一个 `deribitBase`） |
| `exchange/factory` | 带类型的 `XxxConfig`、标的加载器、公共连接器 |
| `gateway/venue` | `Markets` 中的市场 |
| `gateway/account` | `routeClient` 的产品槽位；余额 / 持仓按产品读取 |
| `gateway` 数据集 | 产品专属的常驻数据（永续：资金费率；期权：期权链） |
| 应用 | 产品专属的业务（期权：optioner） |
| `web` | 菜单与页面 |

## 期权

### 标的身份

```
deribit:option:BTC-USD-20261127-73000-P      币本位（Deribit BTC-27NOV26-73000-P）
deribit:option:SOL-USDC-20261023-75-C        U 本位（Deribit SOL_USDC-23OCT26-75-C）
deribit:option:AVAX-USDC-20261020-8.2-C      小数行权价（Deribit 写作 8d2）
okx:option:BTC-USD-20261030-82000-C          OKX 币本位（原生 BTC-USD-261030-82000-C）
okx:option:BTC-USDM-20261106-82000-P         OKX U 本位（原生 BTC-USD_UM-261106-82000-P）
```

规范 symbol 为 `BASE-QUOTE-YYYYMMDD-STRIKE-C|P`，QUOTE 是**行权价币种**。币本位期权的权利金是币本身（`premiumAsset = BTC`），行权价却是美元，所以 QUOTE 写 USD。OKX 的两个合约族都以美元行权、约 400 个条款完全相同，U 本位族在 symbol 中写作 `USDM`（其 quote 资产仍是 USD）以免冲突。

### 合约条款 `OptionTerms`

`underlying`、`kind`（call / put）、`strike`、`expiry`、`premiumAsset`、`inverse`、`index`（结算指数）、`european`、`cashSettled`。范围内的交易所都是欧式现金结算；字段存在是为了让不一样的交易所必须明说，而不是被默认成一样。

### 单位约定（所有交易所一致）

- **数量**：标的数量。Deribit 的 `amount` 对所有期权已经是基础币；步长取 `min_trade_amount`（Deribit 文档明确它是数量步长，与 `contract_size` 不同）。OKX 按张下单，一张 = `ctVal × ctMult` 个标的（BTC 0.01、ETH 0.1 或 0.01、SOL 0.1），适配器在两边换算，交易所的张数不出 exchange 层。
- **价格**：权利金资产每单位标的，即交易所报价与下单的单位。
- **分档最小变动价位**：Deribit 与 OKX 币本位期权便宜时按细档、贵时按粗档报价（BTC：0.005 以下 0.0001，以上 0.0005；OKX 来自 `instrument-tick-bands`）。规则里用 `CoarseTickFrom` / `CoarseTick` 表达，`TickAt(price)` 取适用的档位，下单校验按它对齐。
- **IV**：年化小数（0.42 = 42%）。
- **希腊值**：每单位标的、以**行权价币种（美元）**计。Delta 为标的数量；Gamma 每 1.0 价格变动；Vega 每 1 个波动率点；Theta 每自然日。

### 定价：`optionmath`

一个模型 `Model{Forward, Years, Discount}`：期权价值 = `Discount × Black-76(Forward)`。交易所只在模型参数上不同，不在公式上不同：

| 交易所 | 交易所怎么定价 | 模型 | 实测（标记价还原误差，中位） |
| --- | --- | --- | --- |
| Deribit | 到期日远期（期货或合成），零折现 | `OnForward(F, T)`，Discount = 1 | 0.008%（按指数则 1%） |
| OKX | `opt-summary.fwdPx`，零折现；币本位标记价 = 美元价值 ÷ F | `OnForward(fwdPx, T)` | 0.04%–0.4% |
| Binance | 现货指数 + 交易所的无风险利率 `riskFreeInterest`（Black-Scholes） | `OnSpot(S, r, T)`：F = S·e^{rT}，Discount = e^{-rT} | ≈0（同一时刻推送的 `optionMarkPrice`）；r=0 时误差可达 8% |

报价里 `UnderlyingPrice`（远期）优先；没有远期时用 `IndexPrice` 与 `Rate` 构造现货模型。期权链的每个到期日发布 `forward` 与 `discount`，下游（optioner、前端）直接使用，不再自己猜交易所的约定。

希腊值都是对 S = F·D 求导、每单位标的、美元。Theta 只取波动率衰减项（−S·φ(d1)·σ/(2√T)，每日）：这是交易员说的 Theta，也是 Deribit 与 OKX 发布的口径；带利率时完整 BS Theta 还包含行权价的贴现项，Binance 发布的 Theta 与衰减项更接近。实测还发现 Binance 发布的 Gamma 不是 BS（同一行权价的看涨看跌相差最多 11%），所以 gateway 一律自算、不转发交易所的希腊值。

gateway 用它补全交易所没有发布的数字（买卖 IV、希腊值），应用用它做风险与情景分析，前端有一份同样的 TypeScript 实现（`priceAt` / `impliedVolOf` 带 discount）用于组合预览与按 IV 下单。

### 期权链数据集

gateway 的常驻数据集 `gateway/optionchain`：每 10 秒读取每个期权市场的**全部**期权——Deribit 用 `get_book_summary_by_currency`（BTC、ETH、USDC 三次调用覆盖 4900+ 个合约）；OKX 每个合约族读 `opt-summary`（IV、远期、希腊值）、`tickers`（盘口）、`mark-price`、`open-interest` 四次，加一次美元指数（6 个合约族覆盖 2800+ 个合约）——按**系列**（同一交易所、同一标的、同一权利金资产）发布：

- 每行：买 / 卖 / 标记 / 最新价，标记 / 买 / 卖 IV，Delta / Gamma / Vega / Theta，持仓量，成交量。
- 每个到期日：远期、平值 IV（虚值期权在对数价值度上插值到远期处）、25Δ 风险逆转与 25Δ 蝶式。

它是交易所当前事实的规范化，满足 gateway 的三条原则（可重建、不积累、不做业务）。跨持仓的风险属于应用。

### 单个期权的实时报价

`{"ch":"option","inst":<id>}` 推送该合约的实时报价：买卖价与量、IV、远期、标记价。期权链每 10 秒一刷，下单前看的这一格是实时的。Deribit 是一个 `ticker` 频道；OKX 由 `bbo-tbt`（盘口一档）、`mark-price` 与该合约族共享的 `opt-summary` 订阅合成（同一合约族的多个合约只订阅一次 `opt-summary`）。

### 单个期权交易

期权页的期权链上，点击标记价打开该合约：实时报价与希腊值、带每档 IV 的订单簿、该合约的当前委托与成交，以及一张单合约下单面板——买 / 卖、按价格或按 IV 定价（IV 经该到期日模型换算为权利金，并按买入向下、卖出向上对齐到适用的最小变动价位）、数量以标的计。下单走与现货、永续相同的 gateway 订单接口：一个 clientOrderId，结果未知时按它查询，绝不重发。

期权只接受限价单（OKX 没有期权市价单；Deribit 期权市价单极易滑点）。OKX 的 `tdMode` 由账户模式决定：现货模式 `cash`（只能买入），其他模式 `cross`；期权不支持 reduce-only，适配器在本地拒绝而不是交给交易所。

## 现货扩展

| 交易所 | 现货 | 说明 |
|---|---|---|
| Kraken | 新增 `sdk/krakenspot`：REST + WebSocket v2 | Kraken 现货与 Kraken Futures 是两个平台、两套 Key。一个 Kraken 账户可以填写任一组或两组 |
| Deribit | 复用 `sdk/deribit` | 一个 Key 交易永续、现货、期权；余额是同一组币种钱包，界面只列一次 |
| Gate CrossEx | 复用 `sdk/crossex` | 经纪账户现在也到达 Binance、OKX、Bybit、Gate、Deribit 的现货 |

### 实测依据（2026-10-20）

- Kraken：`AssetPairs` 的 `wsname`（`XBT/USD`）经 `XBT→BTC`、`XDG→DOGE` 两条改名后，与 WebSocket v2 的 1461 个交易对**全部**一致；资产名用 `Assets` 的 `altname` 规范化（硬编码去掉 X/Z 前缀会把 `ZORA`、`XION` 改坏）。
- CrossEx：Binance、OKX、Bybit、Gate 的现货规则行按规范 `BASE-QUOTE` 与原生现货目录**全部**匹配（2939 / 2939）。
- CrossEx 现货市价买单必须以计价币数量（`quote_qty`）下单。gateway 从标的所在交易所读最优卖价换算，向上取整到分；读不到报价时**不发送**，并在错误上标记 `CommandNotSent`。
- Kraken 现货的余额推送不含挂单占用，所以只用 `BalanceEx` 的结果；成交与订单走 `executions` 频道。
- Kraken 现货的 `EService:Unavailable` 不视为拒绝：Kraken 文档说明这种情况下订单可能已经存在。
