# Passkey (WebAuthn) API

Passkeys are per-user credentials. The challenge is held in memory only (single
instance panel, by design) and is single-use. Origin is validated against the
configured site origin — `Host` / `X-Forwarded-Host` headers are not trusted.

## POST /api/v1/user/passkey/status

Passkey availability and the current user's registered passkey count. Public
(pre-login) so the login page can decide whether to offer passkey sign-in.

## POST /api/v1/user/passkey/login/begin

Begin a passkey login ceremony; returns the challenge / options.

## POST /api/v1/user/passkey/login/finish

Finish a passkey login; on success returns the same JWT session as password
login. Disabled accounts are rejected.

## POST /api/v1/user/passkey/register/begin

Begin registering a passkey for the logged-in user.

## POST /api/v1/user/passkey/register/finish

Finish passkey registration.

## POST /api/v1/user/passkey/list

List the current user's passkeys.

## POST /api/v1/user/passkey/delete

Delete one of the current user's passkeys.
