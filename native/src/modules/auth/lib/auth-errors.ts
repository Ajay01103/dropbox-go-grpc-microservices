import { Code, ConnectError } from '@connectrpc/connect';

/**
 * Turns a failed auth call into something worth showing a person.
 *
 * Connect distinguishes transport failures from application ones, and the
 * message the server sends is written for a developer ("invalid credentials"),
 * so the common cases are translated here and anything unrecognized falls back
 * to the server's own text rather than a generic apology.
 */
export function messageForAuthError(error: unknown): string {
  const connectError = ConnectError.from(error);

  switch (connectError.code) {
    case Code.Unauthenticated:
      return 'That email and password do not match. Check them and try again.';
    case Code.AlreadyExists:
      return 'An account with this email already exists. Try logging in instead.';
    case Code.InvalidArgument:
      return connectError.rawMessage || 'Please check the details you entered.';
    case Code.Unavailable:
    case Code.DeadlineExceeded:
      return 'Cannot reach the server. Check your connection and try again.';
    case Code.ResourceExhausted:
      return 'Too many attempts. Wait a moment and try again.';
    default:
      return connectError.rawMessage || 'Something went wrong. Please try again.';
  }
}

/** Field-level validation shared by both auth screens. */
export function validateEmail(email: string): string | null {
  const trimmed = email.trim();
  if (!trimmed) return 'Enter your email address.';
  // Deliberately loose: the server is the authority, this only catches typos.
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(trimmed)) return 'Enter a valid email address.';
  return null;
}

export function validatePassword(password: string): string | null {
  if (!password) return 'Enter your password.';
  if (password.length < 8) return 'Passwords are at least 8 characters.';
  return null;
}

export function validateName(name: string): string | null {
  if (!name.trim()) return 'Enter your name.';
  return null;
}
