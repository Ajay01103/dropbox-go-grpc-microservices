"use client"

import * as React from "react"
import { AlertTriangle, RotateCw } from "lucide-react"

import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

export type DocumentProbeState = "probing" | "ready" | "failed"

export function DocumentLoadingState({
  className,
  label,
}: {
  className?: string
  label: string
}) {
  return (
    <div
      aria-busy="true"
      aria-label={label}
      className={cn("flex h-full w-full items-center justify-center p-6", className)}
    >
      <div className="bg-card flex h-full max-h-full w-full max-w-xl animate-pulse flex-col gap-3 rounded-md border p-6">
        <div className="bg-muted h-3 w-2/3 rounded-full" />
        <div className="bg-muted h-3 w-1/2 rounded-full" />
        <div className="bg-muted h-3 w-3/5 rounded-full" />
      </div>
    </div>
  )
}

export function DocumentErrorState({
  message,
  onRetry,
  retrying,
}: {
  message: string
  onRetry: () => void
  retrying: boolean
}) {
  return (
    <div className="text-muted-foreground flex h-full w-full flex-col items-center justify-center gap-3 p-8 text-center">
      <AlertTriangle className="size-12" strokeWidth={1.5} />
      <p className="text-sm">{message}</p>
      <Button className="gap-1.5" disabled={retrying} onClick={onRetry} size="sm" variant="outline">
        <RotateCw className={cn("h-4 w-4", retrying && "animate-spin")} />
        Try again
      </Button>
    </div>
  )
}

/**
 * Cheap liveness probe for a document URL before a WASM viewer mounts.
 *
 * Content ticket URLs expire after ~2 minutes, and a dead URL surfaces inside a
 * viewer as an opaque load failure — by probing first the panel can tell
 * "expired ticket" apart from "broken file" and refresh the ticket silently
 * once before showing an error.
 *
 * A 5-byte range GET is used because every document route answers Range
 * requests. 206 is the expected reply; 200 means the server ignored the header
 * and will stream the whole file, which is still readable, so only a failure
 * status counts as dead.
 *
 * `documentKey` identifies the open document: opening a different file resets
 * the one-refresh budget.
 */
export function useDocumentProbe({
  documentKey,
  onExpired,
  url,
}: {
  documentKey: string
  onExpired: () => void
  url: string | undefined
}): DocumentProbeState {
  const [probe, setProbe] = React.useState<DocumentProbeState>("probing")
  const refreshedOnceRef = React.useRef(false)

  React.useEffect(() => {
    refreshedOnceRef.current = false
  }, [documentKey])

  React.useEffect(() => {
    if (!url) {
      setProbe("probing")
      return
    }
    let cancelled = false
    setProbe("probing")
    fetch(url, { headers: { Range: "bytes=0-4" } })
      .then((response) => {
        if (cancelled) return
        setProbe(response.ok ? "ready" : "failed")
      })
      .catch(() => {
        if (!cancelled) setProbe("failed")
      })
    return () => {
      cancelled = true
    }
  }, [url])

  // One silent ticket refresh per mounted document. A fresh ticket mints a new
  // URL, which re-runs the probe above; if that fails too the error is real.
  React.useEffect(() => {
    if (probe !== "failed" || refreshedOnceRef.current) return
    refreshedOnceRef.current = true
    onExpired()
  }, [probe, onExpired])

  return probe
}

/**
 * Whether the app itself is rendering in dark appearance. There is no theme
 * provider yet — globals.css keys off the `.dark` class on <html> — so the
 * viewers read that class once on mount. It only seeds their initial state:
 * the night-render toggle inside a viewer affects the rendered document, not
 * the surrounding app.
 */
export function useAppDarkMode(): boolean {
  const [isDark, setIsDark] = React.useState(false)

  React.useEffect(() => {
    const root = document.documentElement
    setIsDark(root.classList.contains("dark"))
    const observer = new MutationObserver(() =>
      setIsDark(root.classList.contains("dark")),
    )
    observer.observe(root, { attributeFilter: ["class"] })
    return () => observer.disconnect()
  }, [])

  return isDark
}
