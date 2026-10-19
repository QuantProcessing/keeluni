# trader

浏览器只访问 trader。trader 本身只有几个接口，其余都是转发。

## 鉴权

| 要求 | 适用 |
|---|---|
| 会话 cookie `keeluni_session` | 除登录、会话查询外的所有接口 |
| 请求头 `X-KeelUni: 1` | 所有非 GET 请求 |
| `Origin` 与站点同源 | 所有非 GET 请求与 WebSocket 握手 |

## 接口

### POST /api/auth/login

`{"password":"…"}` → `{"authenticated":true}`，设置会话 cookie。

| 失败 | HTTP |
|---|---|
| 密码错误 | 401 `UNAUTHENTICATED` |
| 尝试过多 | 429 `THROTTLED` |

### POST /api/auth/logout

清除会话。

### GET /api/auth/session

`{"authenticated":true|false}`

### GET /api/apps

`{"hedge":true|false}`：是否配置了对冲应用（`HEDGER_URL`）。

### 转发

| 浏览器路径 | 目标 |
|---|---|
| `/api/gw/v1/*` | gateway `/v1/*` |
| `GET /api/ws` | gateway `/v1/ws` |
| `/api/hedge/v1/*` | hedger `/v1/*` |
| `GET /api/hedge/ws` | hedger `/v1/ws` |

转发时去掉浏览器的 cookie 与 `X-KeelUni`，加上服务间 bearer token。上游不可达时返回 502 `GATEWAY_UNAVAILABLE` / `HEDGER_UNAVAILABLE`。

## 示例（curl）

```sh
curl -c jar -H 'Origin: http://127.0.0.1:8080' -H 'X-KeelUni: 1' -H 'Content-Type: application/json' \
     -d '{"password":"…"}' http://127.0.0.1:8080/api/auth/login
curl -b jar http://127.0.0.1:8080/api/gw/v1/accounts
```
