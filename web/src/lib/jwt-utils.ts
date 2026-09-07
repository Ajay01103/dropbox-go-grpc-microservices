export function isAccessTokenExpiredOrMissing(
  token: string | undefined,
  skewSeconds = 15,
): boolean {
  if (!token) return true

  try {
    const payloadB64 = token.split(".")[1]
    if (!payloadB64) return true

    const payload = JSON.parse(Buffer.from(payloadB64, "base64url").toString("utf-8")) as {
      exp?: number
    }

    if (!payload.exp) return true

    // Treat as expired slightly before the real exp to avoid races
    // where the token dies mid-request.
    return Date.now() / 1000 >= payload.exp - skewSeconds
  } catch {
    return true
  }
}
