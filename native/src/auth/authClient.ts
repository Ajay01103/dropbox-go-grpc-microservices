import { createClient } from '@connectrpc/connect';

import { AuthService } from '@/gen/pb/auth/auth_pb';
import { authTransport } from './transports';

/**
 * Auth calls that must work while signed OUT, or while the access token is
 * known to be bad: Register, Login, RefreshToken, Logout.
 *
 * Logout belongs here too — it authenticates with the refresh token it is
 * revoking, so it still works when the access token has already expired.
 */
export const authClient = createClient(AuthService, authTransport);
