import { Code, ConnectError, createClient, type Interceptor } from '@connectrpc/connect';

import { AuthService } from '@/gen/pb/auth/auth_pb';
import { FileService, FolderService, MetadataService } from '@/gen/pb/metadata/metadata_pb';
import { UploadService } from '@/gen/pb/upload/upload_pb';
import { env } from '@/lib/env';
import { getValidAccessToken, refreshAccessToken } from '@/auth/tokenManager';
import { createServiceTransport } from '@/auth/transports';

/**
 * The authenticated transport: one interceptor that attaches the bearer token
 * and repairs it exactly once if the server says it is no good.
 *
 * Mirroring web's rpc.ts, the client modules are the single place the app
 * reaches the backend from — screens and hooks never build a transport, and
 * never see a token.
 */

const authInterceptor: Interceptor = (next) => async (request) => {
  const attach = (token: string | null) => {
    if (token) request.header.set('Authorization', `Bearer ${token}`);
  };

  attach(await getValidAccessToken());

  try {
    return await next(request);
  } catch (error) {
    // A stream cannot be replayed once its body has been consumed, and only an
    // authentication failure is worth retrying — everything else is the
    // caller's problem.
    if (request.stream || ConnectError.from(error).code !== Code.Unauthenticated) {
      throw error;
    }
    // The access token expired between minting and the call (or the server
    // restarted its signing key): rotate once and retry the same request.
    attach(await refreshAccessToken());
    return next(request);
  }
};

const authedTransport = (baseUrl: string) => createServiceTransport(baseUrl, [authInterceptor]);

/** Authenticated auth calls: GetCurrentUser, ValidateToken. */
export const authRpcClient = createClient(AuthService, authedTransport(env.authUrl));

export const uploadRpcClient = createClient(UploadService, authedTransport(env.uploadUrl));

export const metadataRpcClient = createClient(
  MetadataService,
  authedTransport(env.metadataUrl)
);

export const metadataFileRpcClient = createClient(
  FileService,
  authedTransport(env.metadataUrl)
);

export const metadataFolderRpcClient = createClient(
  FolderService,
  authedTransport(env.metadataUrl)
);
