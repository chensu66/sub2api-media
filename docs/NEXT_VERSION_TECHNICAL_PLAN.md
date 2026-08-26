# Sub2API Media 下一版本技术规划

状态：规划稿

适用版本：`sub2api-media/v1`（建议下一个发布版本）

配套服务：Gate `media-gateway/v1`

## 1. 目标与非目标

### 1.1 目标

下一版本要把 Sub2API 和 Gate 固定成清晰的两层：

```text
客户端/API Key
        |
        v
Sub2API Media：用户、鉴权、订单、余额、结算、客户状态
        |
        | ES256 service JWT
        v
Gate Media：目录、报价、路由、Provider 执行、归档、产物
```

Sub2API 对外提供稳定的客户 API。Gate 的字段、域名、Provider URL、Provider
任务 ID 和内部路由信息不得成为客户契约的一部分。

### 1.2 非目标

- 不在本版本重写 Gate 的 Provider adapter。
- 不把 Gate 的数据库或对象存储复制到 Sub2API。
- 不立即删除旧的 `/v1/images/*`、`/v1/videos/*` 接口。
- 不让客户端直接访问 Provider 地址或 Gate 内部对象存储。

## 2. 职责边界

| 领域 | Sub2API Media | Gate |
| --- | --- | --- |
| 客户用户、API Key、权限 | 负责 | 不负责 |
| 客户订单、幂等键 | 负责 | 只接收稳定的 `order_id` |
| 钱包 hold/capture/release | 负责 | 只返回明确的结算事实 |
| Media 模型目录 | 读取并转换 | 唯一事实源 |
| 产品匹配与最终报价 | 保存、展示 | 唯一事实源 |
| Provider 路由与容量 | 不负责 | 负责 |
| Provider 提交、轮询、重试 | 不负责 | 负责 |
| 产物归档、校验、保留 | 不负责 | 负责 |
| 客户可见状态 | 负责生成统一投影 | 提供执行事实 |
| 客户产物下载 | 提供同源入口 | 提供内部授权和内容能力 |
| 事件和恢复 | 以本地订单为准，消费 Gate 事实 | 提供带序列号的事实事件 |

核心不变量：

```text
1 个 Sub2API Media order = 1 个 Gate execution = 最多 1 次 Provider acceptance
```

## 3. 公共 API v1 规范

Sub2API 公共接口单独命名为 `sub2api-media/v1`，不得直接复用 Gate 的
`media-gateway/v1` 响应类型。

### 3.1 路由

```text
GET    /v1/media/models
POST   /v1/media/uploads
PUT    /v1/media/uploads/{upload_id}/parts/{part_number}
POST   /v1/media/uploads/{upload_id}/complete
DELETE /v1/media/uploads/{upload_id}
POST   /v1/media/quotes
POST   /v1/media/orders
GET    /v1/media/orders/{order_id}
GET    /v1/media/orders/{order_id}/artifacts/{artifact_id}
GET    /v1/media/orders/{order_id}/artifacts/{artifact_id}/content
```

现有的 artifact GET 路由目前实际是“申请授权”，下一版本保留它以兼容，
但响应必须改成 Sub2API 自己的授权 DTO；真正的二进制读取使用明确的
`/content` 路由。

### 3.2 订单响应

订单响应必须始终返回固定字段，pending 时也返回空的 `artifacts` 数组：

```json
{
  "contract_version": "sub2api-media/v1",
  "object": "media.order",
  "order_id": "media_...",
  "execution_id": "mexec_...",
  "quote_id": "mquote_...",
  "idempotency_key": "client-operation-1",
  "operation": "image.generate",
  "price": { "amount": "0.70000000", "currency": "CNY" },
  "submission_state": "accepted",
  "settlement_state": "captured",
  "projection": {
    "queue_state": "terminal",
    "execution_state": "succeeded",
    "delivery_state": "ready"
  },
  "artifacts": [
    {
      "artifact_id": "mart_...",
      "state": "ready",
      "media_type": "image/png",
      "size_bytes": 123456,
      "content_url": "/v1/media/orders/media_.../artifacts/mart_.../content"
    }
  ],
  "error": null,
  "created_at": "2026-08-25T00:00:00Z",
  "updated_at": "2026-08-25T00:00:03Z"
}
```

规则：

1. `artifacts` 必须在订单顶层，不能只放在 `gate.artifacts`。
2. `projection` 只允许一套客户状态；Gate 原始 projection 不再嵌套返回。
3. `gate` 原始对象暂时保留为兼容字段，但标记 deprecated，不新增依赖。
4. 不返回 `request_hash`、`product`、`route_version`、Provider task ID、Provider URL
   等 Gate 内部字段。
5. 订单状态由 Sub2API 的本地结算和 Gate 的 delivery 事实合并产生；不得把
   `delivery_state=ready` 误写成余额已经 capture。

### 3.3 产物授权和内容

申请授权响应：

```json
{
  "contract_version": "sub2api-media/v1",
  "object": "media.artifact.authorization",
  "artifact_id": "mart_...",
  "action": "read",
  "expires_at": "2026-08-25T00:05:00Z",
  "content_url": "/v1/media/orders/media_.../artifacts/mart_.../content"
}
```

Sub2API 服务端必须完成：

1. API Key 身份验证。
2. `order_id`、`artifact_id` 所属关系验证。
3. artifact 状态为 `ready` 验证。
4. 订单结算状态为 `captured` 验证。
5. 使用服务 JWT 向 Gate 请求 30 至 900 秒授权。
6. 由 Sub2API 代理或受控重定向读取 Gate 内容。

浏览器不应直接收到 Gate 域名的签名 URL。二进制接口必须支持 `GET`、`HEAD`、
`Range`，并转发：

```text
Content-Type
Content-Length
Content-Range
Accept-Ranges
ETag
Cache-Control
Retry-After
```

### 3.4 状态和错误

公共错误统一为：

```json
{
  "error": {
    "type": "media_error",
    "code": "artifact_not_ready",
    "message": "The artifact is not ready for download.",
    "retryable": true,
    "retry_after": 3,
    "request_id": "req_..."
  }
}
```

至少定义：

```text
authentication_error
invalid_request
quote_expired
idempotency_conflict
insufficient_balance
order_not_found
artifact_not_found
artifact_not_ready
settlement_pending
media_unavailable
upstream_unavailable
```

`capture_pending`、`release_pending` 是可观察的中间状态，不得返回“生成失败”。

## 4. Gate 对接规则

### 4.1 报价

Sub2API 将客户请求转发给 Gate 报价，但只保存必要的：

```text
quote_id、operation、model、request_hash、price、expires_at、quote_token
```

`quote_token` 只能存储在 Sub2API 内部，不能返回客户端。

### 4.2 创建执行

Sub2API 使用服务身份调用 Gate：

```text
POST /v1/media/executions
```

请求必须携带稳定的：

```text
order_id
idempotency_key
quote_token
operation
schema_version
request
```

Gate 返回的 `execution_id` 保存到本地订单。网络超时后必须先通过
`GET /v1/media/executions/by-idempotency-key/{key}` 恢复，不得直接创建第二个执行。

### 4.3 状态同步

短期继续使用 Gate execution 查询进行恢复；下一阶段接入 Gate 的签名事件：

```text
GET /v1/media/events?after={cursor}
```

Sub2API 按 `event_id` 去重、按 `sequence` 只接受更新版本。Gate 事件不是客户事件，
Sub2API 需要转换后再决定本地订单和余额状态。

### 4.4 image.edit

本版本继续兼容 Gate 的 `/v1/images/edits` multipart 路径，但只作为内部过渡：

- Sub2API 对客户仍只公开 `/v1/media/*`。
- 返回结果必须转换为统一的 `media.order`。
- 新增测试确保 image.edit 也能产生顶层 `artifacts`。
- 后续 Gate 增加正式的 Media multipart execution 后再迁移。

## 5. 上传安全方案

当前上传代理转发客户原始 API Key，仅作为兼容实现保留。下一阶段改为：

1. Sub2API 验证客户 Key。
2. Sub2API 签发短期 upload assertion。
3. Gate 只验证 assertion 并绑定 `caller_id`、客户主体、upload ID 和过期时间。
4. Gate 不再接收原始客户 Key。

此变更需要和 Gate 同版本发布，不得单边切换。

## 6. 迁移和兼容策略

### 阶段 A：加字段，不破坏旧客户端

- 增加 `contract_version`。
- 增加顶层 `artifacts`。
- 增加同源 `/content`。
- 保留 `gate` 字段。
- 授权响应同时保留旧字段形状和新字段。

### 阶段 B：客户端迁移

- Gate 首页只读取顶层 `artifacts`。
- 新 SDK 只读取 `sub2api-media/v1`。
- 记录仍读取 `gate` 字段的客户端。

### 阶段 C：废弃内部字段

- 在响应和文档中标记 `gate` 为 deprecated。
- 发布 `Deprecation` 和 `Sunset` 响应头。
- 至少保留一个完整小版本周期，再移除原始 Gate 对象。

旧 `/v1/images/*` 和 `/v1/videos/*` 暂不删除，只标记 deprecated；所有新功能必须进入
`/v1/media/*`。

## 7. 必须补充的测试

不产生真实 Provider 费用，全部使用 Gate stub、fixture 和测试数据库：

- quote 成功、过期、重复消费。
- image/video pending 订单。
- `ready + capture_pending`。
- `ready + captured` 且顶层 artifacts 存在。
- failed、released、manual review。
- 相同幂等键重放返回同一订单。
- 不同请求复用幂等键返回 `idempotency_conflict`。
- artifact 不属于订单返回 `404`。
- 未 capture 下载返回 `409` 或 `425`。
- Range、HEAD、ETag、Content-Type 正确。
- Gate 超时后恢复同一个 execution。
- 上传分片响应头完整。

## 8. 发布验收标准

只有同时满足以下条件才发布：

1. 成功 image/video 订单顶层都有 `artifacts`。
2. 浏览器只访问 Sub2API 对外域名，不访问 Gate 签名 URL。
3. 订单状态、结算状态、产物状态可分别解释。
4. 重试不会重复创建 Provider 执行或重复扣费。
5. Gate 不接收客户原始 API Key 的新路径已可用，旧路径仍能回滚。
6. OpenAPI、示例 JSON、迁移说明和契约测试全部提交。
