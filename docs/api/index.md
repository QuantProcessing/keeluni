# API 概览与约定

KeelUni 有三组 HTTP / WebSocket 接口：

| 服务 | 谁调用 | 鉴权 | 文档 |
|---|---|---|---|
| gateway `/v1` | trader（转发浏览器请求）、hedger 等应用 | `Authorization: Bearer <GATEWAY_TOKEN>` | [REST](./gateway-rest)、[WebSocket](./gateway-ws) |
| trader `/api` | 浏览器 | 会话 cookie + `X-KeelUni: 1` + 同源 `Origin` | [trader](./trader) |
| hedger `/v1` | trader（转发浏览器请求） | `Authorization: Bearer <HEDGER_TOKEN>` | [hedger](./hedger) |

浏览器访问 gateway 与 hedger 时经 trader 转发：`/api/gw/v1/*` → gateway `/v1/*`，`/api/hedge/v1/*` → hedger `/v1/*`，`/api/ws` 与 `/api/hedge/ws` 为对应的 WebSocket。

## 通用约定

- **数值是十进制字符串**：价格、数量、余额、费率都以字符串传递（如 `"82817.3"`），避免浮点误差。时间戳是毫秒整数。
- **标的** 用 instrument id：`{venue}:{product}:{symbol}`，见[标识与路由](/architecture/identity)。
- **clientOrderId** 是调用方生成的十进制 uint48 字符串。
- 健康检查 `GET /v1/health` 免鉴权，其余接口都需要鉴权；token 用常量时间比较。
- 请求体是 JSON；未知字段（hedger）会被拒绝。

## 错误

gateway 的错误体：

```json
{"error":{"code":"REJECTED","category":"rejected","message":"Margin is insufficient.",
          "venueCode":"-2019","venueMessage":"Margin is insufficient.","retryable":false}}
```

所有交易所失败按同一套 `category` 分类，调用方不需要了解具体交易所：

| category | 含义 | HTTP | retryable |
|---|---|---|---|
| `network` | 连不上交易所 / 超时 / 交易所 5xx | 502 | 是 |
| `rejected` | 交易所收到并拒绝（保证金不足、数量不合规……） | 422 | 否 |
| `credentials` | 密钥、签名或权限被拒 | 422 | 否 |
| `rate_limited` | 交易所或本地限流 | 429 | 是 |
| `invalid` | 请求在发往交易所前即不合法 | 400 | 否 |
| `unsupported` | 交易所 / 市场不支持该操作 | 501 | 否 |
| `venue_error` | 交易所返回了无法解析的内容 | 502 | 是 |

- `venueMessage` 是交易所自己的说明，原样透传（去除控制字符，最长 240 字符），只来自交易所的业务错误体，绝不来自网络错误、代理页面、请求参数或凭证。
- 创建账户时若为 `network`，表示无法连接交易所，**不代表密钥有误**。

gateway 的其他错误码：`UNAUTHORIZED`（401）、`INVALID_JSON` / `INVALID_REQUEST`（400）、`UNKNOWN_INSTRUMENT`、`NOT_FOUND`（404）、`NO_FUNDING`（该标的暂无资金费率）、`UNSUPPORTED`。

hedger 的拒绝（400）额外带 `error.text`：编码文本，前端按语言渲染（见 [hedger API](./hedger#错误)）。

## 下单的三种结果

`POST /v1/accounts/{id}/orders` 返回 `PlaceResult`：

| status | HTTP | 含义 |
|---|---|---|
| `accepted` | 200 | 已接受，附 `order` |
| `rejected` | 422 | 被拒，附 `category`、`venueCode`、`venueMessage` |
| `unknown` | 202 | 结果未知：**必须**按 clientOrderId 查询，禁止重下 |

详见[订单生命周期](/architecture/orders)。
