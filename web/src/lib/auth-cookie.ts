// __Host- prefix requires Secure + HTTPS — silently rejected by browsers on
// plain HTTP (localhost). Use the prefix in production only.
const isProduction = process.env.NODE_ENV === "production"

export const ACCESS_TOKEN_COOKIE_NAME = isProduction ? "__Host-access_token" : "access_token"

export const REFRESH_TOKEN_COOKIE_NAME = isProduction ? "__Host-refresh_token" : "refresh_token"
