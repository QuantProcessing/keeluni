# 安全

KeelUni 直接操作真实资金，安全设计围绕三件事：凭证不泄露、会话不被劫持、内部服务不暴露。

## 凭证

- **只写不读**：凭证的流向只有一条：浏览器 → trader → gateway → 加密落盘。任何 API 响应、任何日志都不包含凭证。
- **先验证后保存**：创建账户时 gateway 先用凭证构造客户端并向交易所查询余额，成功后才保存。网络失败（`network`）不代表密钥有误，表单保留输入，方便重试。
- **加密存储**：`accounts.enc` 中每条凭证用 AES-256-GCM 单独加密，以「账户 id + 交易所」作为附加数据绑定（换了位置就无法解密）；原子写（临时文件 + fsync + rename），权限 0600。
- **主密钥**：`master.key` 首次启动自动生成（0600）。备份它；丢失后所有账户需要重新连接。
- **最小权限**：只开启交易与读取权限，不开启提现。

::: details 关于加密附加数据中的 `bolter-account-v1`
项目改名为 KeelUni 前，附加数据的标签是 `bolter-account-v1`。它是每条已加密凭证的一部分，修改它会让现有 `accounts.enc` 无法解密，所以保留不变。将来如需改名，需要一次重新加密的迁移。
:::

## trader 会话

| 项 | 做法 |
|---|---|
| 密码 | 首次启动生成随机密码并打印一次；保存 argon2id 哈希；`trader passwd` 重置 |
| 会话 | 32 字节随机 token，只存在于 trader 内存（重启即需重新登录）；cookie `keeluni_session`：`HttpOnly; SameSite=Strict; Path=/`，HTTPS 时 `Secure`；空闲 12 小时、绝对 7 天过期 |
| CSRF | SameSite=Strict；非 GET 请求校验 `Origin` 同源，并要求自定义请求头 `X-KeelUni: 1` |
| WebSocket | 握手时校验 cookie 与 `Origin` |
| 暴力破解 | 按 IP 指数退避（1 秒起，上限 15 分钟），并有全局失败计数 |
| 响应头 | CSP `default-src 'self'`、`X-Frame-Options: DENY`、`Referrer-Policy: no-referrer`、HTTPS 时 HSTS |

## 内部服务

- gateway 与 hedger 只用 bearer token 鉴权（常量时间比较），token 存在 0600 文件中。
- 它们只监听内网（compose 内部网络或 `127.0.0.1`），浏览器的请求全部经 trader 转发，转发时去掉浏览器的 cookie 与 `X-KeelUni` 头，换成服务间 token。
- PostgreSQL 只在内网；超级用户密码只挂载给 PostgreSQL 与 initdb 任务；hedger 使用自己的角色，只能访问自己的库。

## 网络

- 默认只绑定 `127.0.0.1`。
- 远程访问使用 SSH 隧道 / VPN，或前置 TLS 反向代理并设置 `TRADER_SECURE_COOKIE=1`。

## 错误信息中的交易所原文

交易所拒单时，`venueMessage` 原样透传交易所的说明（去除控制字符，最长 240 字符），只来自交易所的业务错误体，绝不来自网络错误、代理页面、请求参数或凭证。
