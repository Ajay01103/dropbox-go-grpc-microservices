import { Code, ConnectError } from '@connectrpc/connect';
import * as SecureStore from 'expo-secure-store';

import { authClient } from './authClient';
import { session } from './session';

/**
 * Token storage policy, and why it is shaped this way:
 *
 * - The refresh token is the only long-lived credential, so it is the only one
 *   that goes to the keychain (SecureStore). It is scoped to this device and
 *   readable after the first unlock, so a background refresh still works.
 * - The access token stays in memory. It is short-lived, and keeping it out of
 *   storage means a stolen device image never yields a usable access token.
 * - Refresh tokens rotate on every refresh, so the rotated pair is persisted
 *   BEFORE the new access token is handed out. If the app dies in between, the
 *   worst case is a re-login rather than a session the server already revoked.
 */

const REFRESH_TOKEN_KEY = 'auth.refresh_token';

/** Refresh this long before expiry so a request never races the clock. */
const EXPIRY_SKEW_MS = 60_000;

/** Used only when an access token carries no readable `exp`, so we refresh. */
const UNKNOWN_EXPIRY_MS = 0;

let accessToken: string | null = null;
let accessTokenExpiresAt = 0;
let inFlightRefresh: Promise<string> | null = null;

/** Decodes a JWT payload without verifying it (the server already did). */
function decodeJwtPayload(token: string): Record<string, unknown> | null {
  const payload = token.split('.')[1];
  if (!payload) return null;
  try {
    const base64 = payload.replace(/-/g, '+').replace(/_/g, '/');
    const padded = base64.padEnd(base64.length + ((4 - (base64.length % 4)) % 4), '=');
    return JSON.parse(globalThis.atob(padded)) as Record<string, unknown>;
  } catch {
    return null;
  }
}

function expiryFromToken(token: string): number {
  const claims = decodeJwtPayload(token);
  const exp = claims?.exp;
  if (typeof exp !== 'number') {
    // No usable expiry: treat the token as already stale so the next call
    // refreshes instead of sending something that might be expired.
    return Date.now() + UNKNOWN_EXPIRY_MS;
  }
  return exp * 1000;
}

/** Stores a freshly issued pair: access in memory, refresh in the keychain. */
export async function setTokens(accessTokenValue: string, refreshToken: string): Promise<void> {
  accessToken = accessTokenValue;
  accessTokenExpiresAt = expiryFromToken(accessTokenValue);
  await SecureStore.setItemAsync(REFRESH_TOKEN_KEY, refreshToken, {
    keychainAccessible: SecureStore.AFTER_FIRST_UNLOCK_THIS_DEVICE_ONLY,
  });
}

export async function getStoredRefreshToken(): Promise<string | null> {
  return SecureStore.getItemAsync(REFRESH_TOKEN_KEY);
}

/** Drops every trace of the session. Used on logout and on a rejected refresh. */
export async function clearTokens(): Promise<void> {
  accessToken = null;
  accessTokenExpiresAt = 0;
  await SecureStore.deleteItemAsync(REFRESH_TOKEN_KEY);
}

export function isNetworkError(error: unknown): boolean {
  const code = ConnectError.from(error).code;
  return code === Code.Unavailable || code === Code.DeadlineExceeded || code === Code.Unknown;
}

/**
 * Resolves the app's starting state once, at launch: a stored refresh token
 * that the server still accepts means the user is signed in.
 *
 * A network failure at launch is NOT a sign-out — the token may be perfectly
 * good and the phone simply offline — so the session stays signed in and the
 * first real request retries the refresh.
 */
export async function bootstrapSession(): Promise<void> {
  let storedRefreshToken: string | null = null;
  try {
    storedRefreshToken = await getStoredRefreshToken();
    if (!storedRefreshToken) {
      session.set('signedOut');
      return;
    }
    await refreshAccessToken();
    session.set('signedIn');
  } catch (error) {
    if (isNetworkError(error) && storedRefreshToken) {
      session.set('signedIn');
      return;
    }
    session.set('signedOut');
  }
}

/**
 * Single-flight refresh: concurrent callers share one in-flight rotation.
 *
 * This matters because refresh tokens rotate — two parallel refreshes would
 * race, and the loser would be presenting a token the server just invalidated,
 * which looks exactly like a stolen token and can revoke the whole family.
 */
export function refreshAccessToken(): Promise<string> {
  inFlightRefresh ??= rotateRefreshToken().finally(() => {
    inFlightRefresh = null;
  });
  return inFlightRefresh;
}

async function rotateRefreshToken(): Promise<string> {
  const refreshToken = await getStoredRefreshToken();
  if (!refreshToken) {
    session.set('signedOut');
    throw new Error('No refresh token is stored.');
  }

  try {
    const response = await authClient.refreshToken({ refreshToken });
    await setTokens(response.accessToken, response.refreshToken);
    return response.accessToken;
  } catch (error) {
    const code = ConnectError.from(error).code;
    if (code === Code.Unauthenticated || code === Code.PermissionDenied) {
      // The server definitively rejected the token: this session is over.
      await clearTokens();
      session.set('signedOut');
    }
    // Anything else (offline, 5xx) leaves the session alone: the token may
    // still be valid and the next request can try again.
    throw error;
  }
}

/**
 * The access token to send on an authenticated call, refreshed ahead of
 * expiry. Returns null when the caller is signed out.
 */
export async function getValidAccessToken(): Promise<string | null> {
  if (accessToken && Date.now() < accessTokenExpiresAt - EXPIRY_SKEW_MS) {
    return accessToken;
  }
  if (session.get() !== 'signedIn') return null;
  return refreshAccessToken();
}
