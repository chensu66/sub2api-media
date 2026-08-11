# Media platform

## Boundary

Sub2API publishes a commercial facade at:

- `GET /v1/media/models`
- `POST /v1/media/quotes`
- `POST /v1/media/orders`
- `GET /v1/media/orders/{order_id}`
- `GET /v1/media/orders/{order_id}/artifacts/{artifact_id}`

The official Sub2API `/v1/images/*` routes are unchanged. Gate's service
routes are never exposed without Sub2API authentication.

Create a standard, non-subscription group with platform `media`, then bind
customer API keys to that group.

## Money and recovery

Gate quotes CNY. For this module, one Sub2API balance unit equals one CNY.
Creating an order reserves the exact signed quote amount using Sub2API's
existing idempotent batch-image balance hold repository.

If the process stops after the balance transaction commits but before the
Media order records `held`, recovery replays the same hold request ID and
finishes the local transition without freezing twice. A definite insufficient
balance result terminates the order and is never retried after a later top-up.

- Gate delivery ready or explicit customer charge: capture the hold.
- Gate terminal and explicitly not charged: release the hold.
- Timeout, connection loss, HTTP 5xx, or unknown Gate state: retain the hold
  and reconcile by the Gate idempotency key.
- Gate HTTP 4xx plus an explicit idempotency lookup miss: release the hold.

Capture also records API key quota and rate-limit usage with a separate,
idempotent billing request. A crash between capture and quota recording is
recovered by replaying both operations.

Reference images are verified against the ordered quote descriptors and
forwarded to Gate for execution. Their bytes are not persisted in Sub2API. A
retried edit submission must therefore send the same ordered files.

## Service identity

Sub2API sends short-lived ES256 assertions to Gate containing:

- `caller_id=sub2api`
- `tenant_subject=sub2api:user:{user_id}`
- `billing_subject=sub2api:api-key:{api_key_id}`
- one least-privilege Media scope

The raw customer API key is never sent to Gate. Gate binds the two opaque
subjects into the signed quote, execution, events, and artifact authorization.

## Configuration

`SUB2API_MEDIA_ENABLED=true` enables the module. The remaining variables are:

- `SUB2API_MEDIA_GATE_BASE_URL` (default `https://gate.ichen.su`)
- `SUB2API_MEDIA_SERVICE_ISSUER`
- `SUB2API_MEDIA_SERVICE_AUDIENCE`
- `SUB2API_MEDIA_CALLER_ID` (default `sub2api`)
- `SUB2API_MEDIA_SERVICE_KEY_ID`
- `SUB2API_MEDIA_SERVICE_PRIVATE_KEY_PEM` or
  `SUB2API_MEDIA_SERVICE_PRIVATE_JWK` (the single-line JWK form is convenient
  for container environments)
- `SUB2API_MEDIA_REQUEST_TIMEOUT` (default `30s`)
- `SUB2API_MEDIA_POLL_INTERVAL` (default `3s`)

Gate must trust the matching public JWK through its existing
`MEDIA_SERVICE_JWKS`, `MEDIA_SERVICE_ISSUER`,
`MEDIA_SERVICE_AUDIENCE`, and `MEDIA_SERVICE_CALLERS` settings.

## Quote and order input

Quote bodies use Gate's `media-gateway/v1` contract unchanged. Sub2API removes
the internal `quote_token` before returning the quote and retains it only for
order admission.

Image generation orders use JSON:

```json
{
  "quote_id": "mquote_...",
  "idempotency_key": "customer-operation-123"
}
```

Image edit orders use `multipart/form-data` with `quote_id`,
`idempotency_key`, and one to sixteen ordered `image[]` parts. File size,
SHA-256, MIME type, and position must match the quote descriptors.
