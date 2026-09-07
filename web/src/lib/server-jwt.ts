import { jwtVerify, type JWTPayload, importJWK, decodeProtectedHeader } from "jose"

type JsonWebKey = Parameters<typeof importJWK>[0]
type SupportedJwsAlgorithm = "EdDSA"

type CachedJwkEntry = {
  key: JsonWebKey
  alg: SupportedJwsAlgorithm
  observedAt: number
}

const JWKS_REFRESH_INTERVAL_MS = 60 * 60 * 1000
const JWKS_RETENTION_MS = 8 * 24 * 60 * 60 * 1000

const jwksCache = new Map<string, CachedJwkEntry>()
let lastJwksFetchAt = 0

export type RefreshTokenValidationResult =
  | { valid: true; payload: JWTPayload }
  | {
      valid: false
      reason:
        | "missing-token"
        | "invalid-token"
        | "invalid-token-type"
        | "invalid-algorithm"
        | "jwks-fetch-failed"
    }

export async function validateRefreshToken(
  token: string | undefined,
): Promise<RefreshTokenValidationResult> {
  if (!token) {
    return { valid: false, reason: "missing-token" }
  }

  try {
    // Decode header to check algorithm without verifying yet
    const header = decodeProtectedHeader(token)
    const algorithm = header.alg as string
    const kid = header.kid as string

    if (!algorithm) {
      return { valid: false, reason: "invalid-token" }
    }

    if (!isSupportedAlgorithm(algorithm)) {
      return { valid: false, reason: "invalid-algorithm" }
    }

    if (!kid) {
      return { valid: false, reason: "invalid-token" }
    }

    const jwksUrl =
      process.env.AUTH_SERVICE_JWKS_URL ||
      process.env.NEXT_PUBLIC_AUTH_JWKS_URL ||
      "http://localhost:50051/.well-known/jwks.json"

    const jwkKey = await resolveRefreshTokenJwk(jwksUrl, kid)
    if ("reason" in jwkKey) {
      return { valid: false, reason: jwkKey.reason }
    }

    try {
      if (jwkKey.alg !== algorithm) {
        return { valid: false, reason: "invalid-token" }
      }

      const publicKey = await importJWK(jwkKey.key as any, algorithm)

      const { payload } = await jwtVerify(token, publicKey, {
        algorithms: [algorithm],
        requiredClaims: ["exp", "iat", "sub", "token_type", "sid", "gen"],
      })

      if (payload.token_type !== "refresh") {
        return { valid: false, reason: "invalid-token-type" }
      }

      if (typeof payload.sid !== "string" || typeof payload.gen !== "number") {
        return { valid: false, reason: "invalid-token" }
      }

      return { valid: true, payload }
    } catch {
      return { valid: false, reason: "invalid-token" }
    }
  } catch {
    return { valid: false, reason: "invalid-token" }
  }
}

type JwkResolutionResult =
  | { key: JsonWebKey; alg: SupportedJwsAlgorithm }
  | { reason: "invalid-token" | "jwks-fetch-failed" }

async function resolveRefreshTokenJwk(jwksUrl: string, kid: string): Promise<JwkResolutionResult> {
  const now = Date.now()
  pruneExpiredJwksEntries(now)

  const cachedEntry = jwksCache.get(kid)
  if (cachedEntry && now - lastJwksFetchAt < JWKS_REFRESH_INTERVAL_MS) {
    return { key: cachedEntry.key, alg: cachedEntry.alg }
  }

  const refreshResult = await refreshJwksCache(jwksUrl, now)
  const refreshedEntry = jwksCache.get(kid)
  if (refreshedEntry) {
    return { key: refreshedEntry.key, alg: refreshedEntry.alg }
  }

  if (cachedEntry) {
    return { key: cachedEntry.key, alg: cachedEntry.alg }
  }

  if (!refreshResult.ok) {
    return { reason: refreshResult.reason }
  }

  return { reason: "invalid-token" }
}

async function refreshJwksCache(
  jwksUrl: string,
  now: number,
): Promise<{ ok: true } | { ok: false; reason: "jwks-fetch-failed" }> {
  try {
    const response = await fetch(jwksUrl, { cache: "no-store" })
    if (!response.ok) {
      return { ok: false, reason: "jwks-fetch-failed" }
    }

    const jwks = await response.json()
    const keys = Array.isArray(jwks?.keys) ? jwks.keys : []

    for (const key of keys) {
      if (!key || typeof key.kid !== "string") continue

      const keyAlg = normalizeJwkAlgorithm(key)
      if (!keyAlg) continue

      jwksCache.set(key.kid, {
        key,
        alg: keyAlg,
        observedAt: now,
      })
    }

    lastJwksFetchAt = now
    pruneExpiredJwksEntries(now)
    return { ok: true }
  } catch {
    return { ok: false, reason: "jwks-fetch-failed" }
  }
}

function pruneExpiredJwksEntries(now: number) {
  for (const [kid, entry] of jwksCache.entries()) {
    if (now - entry.observedAt > JWKS_RETENTION_MS) {
      jwksCache.delete(kid)
    }
  }
}

function isSupportedAlgorithm(algorithm: string): algorithm is SupportedJwsAlgorithm {
  return algorithm === "EdDSA"
}

function normalizeJwkAlgorithm(key: any): SupportedJwsAlgorithm | null {
  if (key?.alg === "EdDSA" || (key?.kty === "OKP" && key?.crv === "Ed25519")) {
    return "EdDSA"
  }

  return null
}
