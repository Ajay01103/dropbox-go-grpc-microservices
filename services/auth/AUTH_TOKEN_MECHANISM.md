# Auth Token and Session Mechanism

This document describes how the auth service generates tokens, anchors them to sessions, detects refresh replay/theft, and applies revocation.

## Graph-Located Source Files

The graph pointed to these core files for this mechanism:

- [services/auth/cmd/main.go](cmd/main.go)
- [services/auth/internal/service/auth_service.go](internal/service/auth_service.go)
- [services/auth/internal/scyllastore/session_store.go](internal/scyllastore/session_store.go)
- [services/auth/db/migrations/1_initial_schema.cql](db/migrations/1_initial_schema.cql)
- [pkg/token/eddsa_maker.go](../../pkg/token/eddsa_maker.go)
- [pkg/token/jwt_maker.go](../../pkg/token/jwt_maker.go)
- [pkg/token/payload.go](../../pkg/token/payload.go)
- [pkg/token/maker.go](../../pkg/token/maker.go)
- [services/auth/server/grpc.go](server/grpc.go)
- [services/auth/internal/tokencache/cache.go](internal/tokencache/cache.go)

## High-Level Model

The auth service does not treat access and refresh tokens as standalone strings. They are session-anchored JWTs.

Each token carries:

- `sub` = user ID
- `sid` = session ID
- `gen` = session generation counter
- `gv` = user global revocation version
- `email`, `name`
- `token_type` = access or refresh
- `iat`, `exp`
- `kid` = signing key ID for EdDSA tokens

The session ID and generation are the important anchor:

- `sid` ties both tokens to one server-side session row
- `gen` changes on every refresh rotation
- `gv` changes when the user is globally revoked

That means the service can reject stolen or stale tokens even though the JWT itself is stateless.

## Token Generation Flow

### 1. Startup wires the signing backend

In [services/auth/cmd/main.go](cmd/main.go), the service starts by:

- connecting to ScyllaDB
- running migrations
- creating a `SessionStore`
- creating a `RevocationStore`
- creating a shared Ristretto cache for session and revocation state
- creating an EdDSA token maker with `NewEDDSAMakerWithScylla(session, eddsaKeyRetention)`

So the active signing backend is EdDSA, not HMAC.

### 2. Login/register mint the first session

In [services/auth/internal/service/auth_service.go](internal/service/auth_service.go), both `Register` and `Login` call `mintSessionTokenPair`.

That helper:

- generates a new `sessionID`
- creates a `user_sessions` row with `gen = 1`
- reads the current `user_revocations` row to get `global_ver`
- mints a session refresh token
- mints a session access token
- stores the session state in cache

So the first token pair is always tied to a persisted session row.

### 3. Signing and claims

The token makers in [pkg/token/eddsa_maker.go](../../pkg/token/eddsa_maker.go) and [pkg/token/jwt_maker.go](../../pkg/token/jwt_maker.go) create the JWTs.

The EdDSA maker is the one used by the auth service startup. It:

- signs tokens with Ed25519 / EdDSA
- embeds the active `kid` in the JWT header
- exposes JWKS so other services can validate tokens
- loads and persists signing keys in ScyllaDB

The actual claim payloads live in [pkg/token/payload.go](../../pkg/token/payload.go):

- `AccessPayload`
- `RefreshPayload`
- `SessionAccessPayload`
- `SessionRefreshPayload`

The session-mode token creators are:

- `CreateSessionRefreshToken`
- `CreateSessionAccessToken`

The corresponding validators are:

- `VerifySessionRefreshToken`
- `VerifySessionAccessToken`

## Session Anchoring

### Session row

The session anchor is stored in ScyllaDB table `user_sessions`.

Fields used by the auth service:

- `user_id`
- `session_id`
- `gen`
- `device_fp`
- `expires_at`
- `created_at`
- `updated_at`

The important field is `gen`.

### Revocation row

Global sign-out is tracked in `user_revocations`.

Fields used by the auth service:

- `user_id`
- `global_ver`
- `created_at`
- `updated_at`

The important field is `global_ver`.

### Cache layer

The auth service caches three kinds of state in Ristretto via [services/auth/internal/tokencache/cache.go](internal/tokencache/cache.go):

- `sess:<sessionID>` for session generation state
- `gver:<userID>` for user-wide revocation state
- `cur:<userID>` for current user profile data

The session cache TTL is short and intentionally smaller than the JWT lifetime so refresh validation can be fast without becoming the source of truth.

## Refresh Token Rotation

### Refresh path

In [services/auth/internal/service/auth_service.go](internal/service/auth_service.go), `RefreshToken` does the following:

1. Parse and verify the refresh token with `VerifySessionRefreshToken`.
2. Read `sid`, `gen`, and `gv` from the token payload.
3. Check the session cache first.
4. If the cache has a matching session entry, verify:
   - `payload.Gen == cached.Gen`
   - `payload.GlobalVer >= cached.MinGlobalVer`
5. If the cache misses, read the session row from Scylla.
6. Verify:
   - session exists
   - session is not expired
   - `payload.Gen == session.Gen`
   - `payload.GlobalVer >= current user revocation version`
7. Bump the session generation with `AtomicBumpGen`.
8. Mint a new refresh token and new access token.
9. Update the cache with the new generation.

### Why this is secure

A refresh token is only valid if all of the following still match:

- the session row still exists
- the generation matches the current server-side generation
- the global version has not been bumped past the token

That means an old refresh token cannot be replayed after a successful rotation.

## Refresh Token Theft / Replay Detection

### What counts as replay

Replay is detected when an incoming refresh token has a `gen` value that does not match the server-side session generation.

That happens when:

- the token is stale after a previous refresh rotation
- the token was copied and reused by an attacker
- a race produces an older token arriving after a newer one

### Detection logic

`RefreshToken` checks the generation in two places:

- cache hit path
- Scylla fallback path

If the generation mismatches, the service returns `ErrReplayDetected` and triggers `handleTheftDetected`.

### The theft action

`handleTheftDetected` performs the cleanup:

- logs a high-severity warning
- deletes the compromised session row from `user_sessions`
- evicts the session from the cache

Repeated replay detections are counted in the auth cache, and once the threshold is reached the service bumps `global_ver` for the user as an account-level escalation.

This is a session-kill response, not a full-account kill.

### Important briefing

- The current mechanism is generation-based replay detection, not a refresh-family table model.
- There is no dedicated refresh token table in the current schema.
- `ErrTokenReuseDetected` and `ErrRefreshFamilyMissing` exist in the service error set and server mapping, but the active refresh path in this codebase is the session generation mechanism described above.
- The code comments explicitly note a possible future improvement to bump `global_ver` on repeated theft, but that is not the current behavior.

## Access Token Validation

Access tokens are validated in `ValidateToken`.

The flow is similar, but read-only:

1. Verify the session access JWT signature and claims.
2. Check cache for the session state.
3. If the cache is warm, validate `gen` and `gv` against cached values.
4. If the cache misses, read the session row and revocation row from Scylla.
5. Reject the token if the session is missing, expired, or revoked.

Access tokens are not rotated by themselves. They become invalid when the underlying session generation or global revocation state changes.

## Logout and Global Revocation

### Logout

`Logout` verifies the refresh token and clears the session cache entry.

The current code comments note that explicit session deletion in Scylla is optional for this path because TTL will eventually expire the row.

### LogoutAllDevices

`LogoutAllDevices` bumps the user-wide `global_ver` through the revocation store.

That invalidates:

- all access tokens for the user
- all refresh tokens for the user
- all session generations with a lower global version

This is the global kill switch.

## Database Tables

| Table               | Purpose                                          | Important Columns                                          | Notes                                       |
| ------------------- | ------------------------------------------------ | ---------------------------------------------------------- | ------------------------------------------- |
| `user_sessions`     | Session anchor for refresh/access token rotation | `user_id`, `session_id`, `gen`, `expires_at`               | `gen` is the replay detector; TTL is 7 days |
| `user_revocations`  | User-wide logout / revocation version            | `user_id`, `global_ver`                                    | Bumped by logout-all-devices                |
| `signing_keys`      | EdDSA signing key storage                        | `kid`, `private_key`, `public_key`, `status`, `expires_at` | Used by the token maker and JWKS export     |
| `jwks_public_keys`  | Cached JWKS response                             | `id`, `jwks_json`, `version`                               | Served by `/.well-known/jwks.json`          |
| `users`             | Canonical user records                           | `id`, `email`, `name`, `password`                          | Used by register/login/profile paths        |
| `users_by_email_mv` | Email lookup view                                | `email`, `id`                                              | Supports login lookup                       |

## Token / Key Lifecycle Summary

- Login/register creates a new session row and a new JWT pair.
- Refresh rotates the session generation and mints a new pair.
- Replay is detected by a generation mismatch.
- Theft response deletes the compromised session and clears cache state.
- Logout-all bumps the user global version.
- Signing keys are EdDSA keys stored in Scylla and published through JWKS.

## Practical Notes

- The JWTs themselves are not stored in the database.
- The server stores only session state, revocation state, and signing key material.
- The access token is a short-lived bearer credential; the refresh token is the rotating session credential.
- The session generation counter is the main anti-replay control.
- Cache misses are safe because Scylla remains the source of truth.
