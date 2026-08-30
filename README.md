# mail2bark

mail2bark 是一个面向设备和监控系统的告警网关：它接收 SMTP 邮件，提取告警重点，再发送到 Bark，让告警直接出现在 iPhone 通知栏。

## Bark 是什么

Bark 是一个开源的 iOS 推送工具：在 iPhone 上安装 Bark App 后会生成一个 Device Key，任何设备都可以通过一个简单的 HTTP 请求把消息推送到这台 iPhone。

- Bark 项目地址：<https://github.com/Finb/Bark>
- 官方推送服务：`https://api.day.app`，免费，适合个人使用
- 也可以自建 Bark Server，mail2bark 支持填写任意 Bark 服务器地址

## 快速开始

```sh
cp .env.example .env
docker compose up -d --build
```

打开 `http://127.0.0.1:8080/` 进入管理界面。默认不启用登录，适合内网部署；如果服务需要暴露到公网，请设置 `MAIL2BARK_ADMIN_KEY`，并同时使用防火墙或反向代理限制访问。

`main` 分支推送后会发布 `ghcr.io/eterluu/mail2bark:latest` 和形如 `r260716.095432` 的新加坡时间标签。GHCR 自动保留最近 5 个容器版本并删除更旧版本。

## 配置发送来源

先在 iPhone 上安装 Bark App，打开后复制自己的 Device Key。

然后按顺序在管理界面完成两步配置：

1. 添加 Bark 设备
   - 名称：例如“办公室 iPhone”
   - 服务器：官方服务填 `https://api.day.app`，自建服务填自己的地址
   - Device Key：从 Bark App 复制
2. 创建接入 API Key
   - 来源名称：例如 `idrac-r740`
   - 允许的来源 IP：设备或监控服务的出口 IP/CIDR
   - 选择刚才添加的 Bark 设备

创建完成后，系统会自动生成一个内部收件地址，例如 `idrac-r740-7f3c@notify.internal`，并且 API Key 只显示一次，请立即保存。

在 iDRAC、IPMI、QNAP 或监控服务中配置 SMTP：

```text
SMTP 服务器：运行 mail2bark 的主机地址
SMTP 端口：默认 2525；如修改过端口映射，填写实际端口
SMTP 用户名：mail2bark
SMTP 密码：创建 API Key 时生成的 Key
收件地址：页面生成的内部地址
```

SMTP 用户名始终是 `mail2bark`。Key 同时绑定来源 IP、内部收件地址和 Bark 设备，邮件必须同时通过全部校验才会被接收；未授权客户端、错误 Key、未知收件地址和外部收件地址都会被拒绝。填写单个 IP 时，系统会自动转换成 `/32` 或 `/128`。

### 使用 API 配置

先创建 Bark 设备，把返回的 ID 用于创建 Key：

```sh
curl -X POST http://127.0.0.1:8080/v1/destinations \
  -H 'Content-Type: application/json' \
  -d '{"name":"办公室 iPhone","server":"https://api.day.app","device_key":"BARK_KEY","group":"infrastructure","sound":"alarm","level":"active"}'

curl -X POST http://127.0.0.1:8080/v1/smtp/credentials \
  -H 'Content-Type: application/json' \
  -d '{"name":"idrac-r740","allowed_ips":["192.168.10.30/32"],"destination_id":1}'
```

如果设置了管理员密钥，所有 API 请求都需要增加：

```http
Authorization: Bearer <MAIL2BARK_ADMIN_KEY>
```

## 邮件与通知

服务优先读取纯文本邮件；没有纯文本时，会自动把 HTML 转换为纯文本。邮件中的级别、设备、组件、事件和时间会被提取到通知前部，正文会控制在适合 iOS 通知栏的长度，完整原始邮件仍保存在 SQLite 中。

邮件写入数据库成功后，SMTP 才会收到 `250 OK`。Bark 网络错误、超时、429 和 5xx 会自动重试；多次失败后进入死信，可在“邮件记录”页面手动重新投递。

## HTTPS 与 SMTP TLS

默认 `docker compose up` 只启动 mail2bark，不启动 Caddy。需要管理界面 HTTPS 时再启用：

```sh
MAIL2BARK_DOMAIN=notify.example.com docker compose --profile tls up -d
```

SMTP TLS 由 mail2bark 直接提供。默认 `/certs` 使用 Docker named volume，不要求宿主机存在 `certs` 目录。设置以下参数即可启用相应监听：

```env
MAIL2BARK_TLS_CERT=/certs/fullchain.pem
MAIL2BARK_TLS_KEY=/certs/privkey.pem
MAIL2BARK_SMTP_STARTTLS_ADDR=:587
MAIL2BARK_SMTP_TLS_ADDR=:465
```

## 子路径反向代理

管理界面可以挂载到任意子路径。例如使用 `https://example.com/mail2bark/` 时，可选择以下任一代理模式。

保留前缀转发时，mail2bark 需要知道该前缀：

```env
MAIL2BARK_BASE_PATH=/mail2bark
```

```caddyfile
example.com {
    redir /mail2bark /mail2bark/ 308

    handle /mail2bark/* {
        reverse_proxy mail2bark:8080
    }
}
```

由代理剥离前缀时，不要求设置 `MAIL2BARK_BASE_PATH`：

```caddyfile
example.com {
    redir /mail2bark /mail2bark/ 308

    handle_path /mail2bark/* {
        reverse_proxy mail2bark:8080
    }
}
```

`/mail2bark` 可以替换为其他路径。服务在配置子路径后仍保留根路由，因此两种模式也可以随时切换。根路径的 `/healthz` 和 `/readyz` 始终可用于容器健康检查。SMTP 端口不是 HTTP 流量，仍需直接映射或使用 TCP 代理。

## 数据与接口

- `GET /v1/messages`：查看最近邮件及投递状态
- `GET /v1/messages/{id}`：查看邮件详情、解析结果和原始邮件
- `DELETE /v1/messages/{id}`：删除非投递中的邮件
- `POST /v1/messages/{id}/retry`：重新投递死信
- `GET/POST /v1/smtp/credentials`：查看或创建接入 API Key
- `GET /v1/smtp/credentials/{id}`：查看接入配置和当前 API Key
- `PUT/DELETE /v1/smtp/credentials/{id}`：修改或删除接入 API Key
- `POST /v1/smtp/credentials/{id}/rotate`：轮换 SMTP API Key，自动收件地址保持不变
- `POST /v1/smtp/credentials/{id}/test`：校验 SMTP API Key 并将测试邮件加入 Bark 投递队列
- `GET/POST /v1/destinations`：查看或创建 Bark 设备
- `PUT/DELETE /v1/destinations/{id}`：修改或删除 Bark 设备

管理界面的 SMTP 测试会验证 Key、邮件解析、持久化和 Bark 投递，不会检查外部设备到 SMTP 监听端口之间的网络、防火墙或 TLS 配置。仍被接入 Key 引用的 Bark 设备不能删除；仍有待处理邮件的 Key 也会受到删除保护。

新创建或轮换的 API Key 可以通过详情 API 再次查看；旧版本数据库中的 Key 哈希无法反推出原值，升级后需要先轮换一次。轮换不会改变自动生成的 SMTP 收件地址。

SQLite 使用 WAL 和 FULL synchronous。数据库文件位于 `/data/mail2bark.db`，建议将 `/data` 持久化到 Docker volume 或主机备份目录。
