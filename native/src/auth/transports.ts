import { createGrpcWebTransport } from '@connectrpc/connect-web';
import type { Interceptor } from '@connectrpc/connect';

import { env } from '@/lib/env';

/**
 * Every transport in the app is gRPC-Web over HTTP/1.1 with the binary format.
 *
 * The Go services are connectrpc servers behind h2c: they answer gRPC-Web but
 * not the Connect protocol's `application/connect+proto` content type, which is
 * the same reason web's rpc.ts picks gRPC-Web for the browser → Next.js leg.
 *
 * `interceptors` is threaded through here so the unauthenticated auth transport
 * (register/login/refresh) and the authenticated API transport can be built
 * from one place without either importing the other.
 */
export function createServiceTransport(baseUrl: string, interceptors: Interceptor[] = []) {
  return createGrpcWebTransport({
    baseUrl,
    useBinaryFormat: true,
    interceptors,
  });
}

/**
 * The unauthenticated auth transport. It never attaches an access token: the
 * token manager needs a way to call RefreshToken/Login that cannot deadlock on
 * the token it is trying to obtain.
 */
export const authTransport = createServiceTransport(env.authUrl);
