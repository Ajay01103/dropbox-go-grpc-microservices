import { clsx, type ClassValue } from "clsx"
import { twMerge } from "tailwind-merge"

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

// formatBytes renders a byte count (proto int64 arrives as bigint) with a
// sensible unit; null/undefined/0 renders as "0 B".
export function formatBytes(bytes: bigint | number | null | undefined): string {
  const value = typeof bytes === "bigint" ? Number(bytes) : (bytes ?? 0)
  if (!Number.isFinite(value) || value <= 0) return "0 B"
  const units = ["B", "KB", "MB", "GB", "TB"] as const
  const exp = Math.min(Math.floor(Math.log2(value) / 10), units.length - 1)
  const scaled = value / 1024 ** exp
  const digits = scaled >= 100 || exp === 0 ? 0 : 1
  return `${scaled.toFixed(digits)} ${units[exp]}`
}

// formatRelativeTime renders an ISO timestamp (as produced by the Go
// services) as a coarse human distance, e.g. "just now" or "3 days ago".
// Unknown/invalid input renders as an empty string so callers can inline it.
export function formatRelativeTime(iso: string | null | undefined): string {
  if (!iso) return ""
  const date = new Date(iso)
  const seconds = Math.round((date.getTime() - Date.now()) / 1000)
  if (!Number.isFinite(seconds)) return ""
  const abs = Math.abs(seconds)
  if (abs < 60) return "just now"

  const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" })
  if (abs < 3600) return rtf.format(Math.round(seconds / 60), "minute")
  if (abs < 86400) return rtf.format(Math.round(seconds / 3600), "hour")
  if (abs < 86400 * 30) return rtf.format(Math.round(seconds / 86400), "day")
  if (abs < 86400 * 365) return rtf.format(Math.round(seconds / (86400 * 30)), "month")
  return rtf.format(Math.round(seconds / (86400 * 365)), "year")
}
