import type { ComponentProps } from 'react';
import { MaterialCommunityIcons } from '@expo/vector-icons';

import type { File } from '@/gen/pb/metadata/metadata_pb';

type IconName = ComponentProps<typeof MaterialCommunityIcons>['name'];

/** The filled blue folder of the reference design, shared by every surface. */
export const FOLDER_TINT = '#9CC3F7';

/**
 * Presentation helpers for file rows and tiles.
 *
 * Sizes arrive as bigint (protobuf int64), timestamps as the RFC3339 strings the
 * Go services emit. Both are formatted here so no screen has to guess at a
 * unit, and so the flat list can compare strings instead of Date objects.
 */

export function formatBytes(bytes: bigint | number | null | undefined): string {
  const value = typeof bytes === 'bigint' ? Number(bytes) : (bytes ?? 0);
  if (!Number.isFinite(value) || value <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'] as const;
  const exponent = Math.min(Math.floor(Math.log2(value) / 10), units.length - 1);
  const scaled = value / 1024 ** exponent;
  const digits = scaled >= 100 || exponent === 0 ? 0 : 1;
  return `${scaled.toFixed(digits)} ${units[exponent]}`;
}

/**
 * A coarse human distance, e.g. "just now" or "3 days ago".
 *
 * Written by hand rather than with Intl.RelativeTimeFormat: Hermes ships Intl
 * on Android but not consistently everywhere, and a crash on one device for a
 * subtitle is not worth the locale fidelity. Invalid input renders as an empty
 * string so callers can inline it after a separator.
 */
export function formatRelativeTime(iso: string | null | undefined): string {
  if (!iso) return '';
  const ms = Date.parse(iso);
  if (!Number.isFinite(ms)) return '';

  const seconds = Math.round((ms - Date.now()) / 1000);
  const abs = Math.abs(seconds);
  const ago = (value: number, unit: string) =>
    `${value} ${unit}${value === 1 ? '' : 's'} ago`;

  if (abs < 45) return 'just now';
  if (abs < 3600) return ago(Math.max(1, Math.round(abs / 60)), 'minute');
  if (abs < 86400) return ago(Math.round(abs / 3600), 'hour');
  if (abs < 86400 * 30) return ago(Math.round(abs / 86400), 'day');
  if (abs < 86400 * 365) return ago(Math.round(abs / (86400 * 30)), 'month');
  return ago(Math.round(abs / (86400 * 365)), 'year');
}

export type FileKind = 'image' | 'video' | 'pdf' | 'document' | 'sheet' | 'archive' | 'other';

export function fileKind(file: Pick<File, 'contentType' | 'filename'> | undefined): FileKind {
  const type = file?.contentType?.toLowerCase() ?? '';
  const name = file?.filename?.toLowerCase() ?? '';

  if (type.startsWith('image/')) return 'image';
  if (type.startsWith('video/')) return 'video';
  if (type === 'application/pdf' || name.endsWith('.pdf')) return 'pdf';
  if (name.endsWith('.doc') || name.endsWith('.docx')) return 'document';
  if (name.endsWith('.xls') || name.endsWith('.xlsx') || name.endsWith('.csv')) return 'sheet';
  if (name.endsWith('.zip') || name.endsWith('.tar') || name.endsWith('.gz')) return 'archive';
  return 'other';
}

/** The icon a tile falls back to when no thumbnail is available. */
export function fileKindIcon(kind: FileKind): { name: IconName; color: string } {
  switch (kind) {
    case 'image':
      return { name: 'image-outline', color: '#6B7280' };
    case 'video':
      return { name: 'play-circle-outline', color: '#6B7280' };
    case 'pdf':
      return { name: 'file-pdf-box', color: '#DC2626' };
    case 'document':
      return { name: 'file-word-outline', color: '#2563EB' };
    case 'sheet':
      return { name: 'file-excel-outline', color: '#15803D' };
    case 'archive':
      return { name: 'folder-zip-outline', color: '#6B7280' };
    default:
      return { name: 'file-document-outline', color: '#6B7280' };
  }
}

/**
 * The one-line description under a file name: "1.2 MB · 3 days ago".
 *
 * The metadata service exposes creation time only, so the second part is when
 * the file was added, not a separate "modified" timestamp.
 */
export function describeFile(
  file: Pick<File, 'sizeBytes' | 'createdAt'> | undefined,
  separator = ' · ',
): string {
  if (!file) return '';
  return [formatBytes(file.sizeBytes), formatRelativeTime(file.createdAt)]
    .filter(Boolean)
    .join(separator);
}
