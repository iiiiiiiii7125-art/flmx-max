# Authentication API

## POST /api/v1/user/login

Authenticate and obtain JWT token.

**Request:**
```json
{
  "username": "admin",
  "password": "secret",
  "captchaId": "optional-captcha-id"
}
```

**Response:**
```json
{
  "code": 0,
  "msg": "操作成功",
  "data": {
    "token": "eyJhbGciOiJIUzI1NiIs...",
    "name": "admin",
    "role_id": 0,
    "requirePasswordChange": false
  },
  "ts": 1706659200000
}
```

**Response Fields:**

| Field | Type | Description |
|-------|------|-------------|
| token | string | JWT token for subsequent requests |
| name | string | The account's username |
| role_id | number | 0 = admin, 1 = regular user |
| requirePasswordChange | boolean | Whether password change is required (restricted token: only `user/updatePassword` is reachable until changed) |

## JWT Token Details

**Algorithm:** HMAC-SHA256 (header `alg` is the literal `HmacSHA256`)
**Lifetime:** 7 days

**Token Claims:**
```json
{
  "sub": "1",
  "user": "admin",
  "name": "admin",
  "role_id": 0,
  "iat": 1706659200,
  "iat_ms": 1706659200000,
  "exp": 1707264000
}
```

Restricted tokens (default-password login) additionally carry
`"pwd_change_required": true`.

## POST /api/v1/captcha/check

Check if captcha verification is required.

**Request:** `{}`

**Response:** `data` is a bare number — `1` when login captcha is
enabled, `0` when it is not:

```json
{
  "code": 0,
  "data": 1
}
```

## POST /api/v1/captcha/verify

Verify captcha response (Cloudflare Turnstile or local captcha).

**Request:**
```json
{
  "captchaId": "captcha-session-id",
  "captchaValue": "user-captcha-response"
}
```

**Response:** unified envelope `{code, msg, data, ts}` — `code == 0` means success.

```json
{
  "code": 0,
  "msg": "操作成功",
  "data": { "validToken": "captcha-session-id" },
  "ts": 1792383219661
}
```

> Breaking change (R5-B2): the old bare shape `{success, data: {validToken}}`
> was removed. Migrate by checking `code == 0` instead of `success == true`.

## Token Usage

Include the token in all authenticated requests:

```bash
curl -X POST "${FLMX_BASE_URL}/api/v1/node/list" \
  -H "Authorization: eyJhbGciOiJIUzI1NiIs..." \
  -H "Content-Type: application/json" \
  -d '{}'
```

⚠️ **CRITICAL: Do NOT add "Bearer " prefix!**

```
✅ Correct:   Authorization: eyJhbGciOiJIUzI1NiIs...
❌ Incorrect: Authorization: Bearer eyJhbGciOiJIUzI1NiIs...
```

## POST /api/v1/user/updatePassword

Change current user's password.

**Request:**
```json
{
  "oldPassword": "current-password",
  "newPassword": "new-password"
}
```

**Response:**
```json
{"code": 0, "msg": "操作成功"}
```

## POST /api/v1/user/package

Get current user's package info (tunnels, forwards, traffic stats).

**Request:** `{}`

**Response:** `data` groups the account, its tunnel assignments, its
forwards and hourly statistics:

```json
{
  "code": 0,
  "data": {
    "userInfo": {
      "id": 2,
      "name": "alice",
      "user": "alice",
      "status": 1,
      "flow": 100,
      "flowMiB": 0,
      "inFlow": 1073741824,
      "outFlow": 2147483648,
      "num": 10,
      "expTime": 1735689600000,
      "flowResetTime": 1,
      "createdTime": 1706659200000,
      "updatedTime": 1706659200000
    },
    "tunnelPermissions": [
      {"id": 1, "userId": 2, "tunnelId": 1, "tunnelName": "HK-US",
       "tunnelFlow": 50, "flow": 50, "flowMiB": 0, "inFlow": 0,
       "outFlow": 0, "num": 0, "flowResetTime": 1, "expTime": 0,
       "speedId": null, "speedLimitName": null, "speed": null}
    ],
    "forwards": [
      {"id": 1, "name": "web", "tunnelId": 1, "tunnelName": "HK-US",
       "inIp": "", "inPort": 10001, "remoteAddr": "10.0.0.1:80",
       "inFlow": 0, "outFlow": 0, "status": 1, "createdTime": 1706659200000}
    ],
    "statisticsFlows": []
  }
}
```

**userInfo fields:**

| Field | Type | Description |
|-------|------|-------------|
| flow | number | Total traffic quota in GB |
| inFlow | number | Used upload in bytes |
| outFlow | number | Used download in bytes |
| num | number | Max forwards allowed |
| expTime | number | Account expiry timestamp (ms) |
