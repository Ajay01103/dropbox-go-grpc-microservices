import { Platform } from 'react-native';

/**
 * The Go services run as three separate connect-rpc servers, and a dev build
 * talks to them directly (native has no Next.js proxy in front of it the way
 * web does).
 *
 * A phone can never reach the dev machine's `localhost`, so the fallback host
 * depends on where the app is running:
 *
 * - Android emulator: `10.0.2.2` is the host loopback.
 * - iOS simulator / web: `localhost` is the host.
 * - Physical device: neither works — set EXPO_PUBLIC_*_URL to the machine's
 *   LAN address (see .env.example).
 */

const devHost = Platform.select({ android: '10.0.2.2', default: 'localhost' });

function serviceUrl(explicit: string | undefined, port: number): string {
  const trimmed = explicit?.trim();
  if (trimmed) return trimmed.replace(/\/+$/, '');
  return `http://${devHost}:${port}`;
}

export const env = {
  /** Auth service: register, login, refresh, logout, current user. */
  authUrl: serviceUrl(process.env.EXPO_PUBLIC_AUTH_URL, 50051),
  /** Upload service: upload sessions, blocks, edit commits. */
  uploadUrl: serviceUrl(process.env.EXPO_PUBLIC_UPLOAD_URL, 50052),
  /** Metadata service: files, folders, previews, download tickets. */
  metadataUrl: serviceUrl(process.env.EXPO_PUBLIC_METADATA_URL, 50053),
} as const;
