"use client"

import { useEffect, useMemo, useState } from "react"
import type { Body, Meta, UppyFile } from "@uppy/core"
import { AlertCircle, Check, ChevronDown, File as FileIcon, Upload, X } from "lucide-react"

import { Button } from "@/components/ui/button"
import { useAuth } from "@/lib/auth-context"
import { useUppyFileUpload } from "../hooks/use-uppy-file-upload"

const OPEN_UPLOAD_EVENT = "dropbox:open-upload"
type UploadFile = UppyFile<Meta, Body>
type UploadTab = "all" | "completed" | "failed"

function fileType(file: UploadFile) {
  const name = file.name ?? ""
  return name.includes(".") ? name.split(".").pop()?.toUpperCase() : "FILE"
}

function isComplete(file: UploadFile) {
  return file.progress?.uploadComplete === true
}

function isFailed(file: UploadFile) {
  return Boolean(file.error)
}

function FileRow({ file }: { file: UploadFile }) {
  const complete = isComplete(file)
  const failed = isFailed(file)
  const progress = Math.round(file.progress?.percentage ?? 0)

  return (
    <div className="flex items-start gap-3 px-4 py-3">
      <div className="mt-0.5 flex size-9 shrink-0 items-center justify-center rounded-md bg-neutral-800">
        <FileIcon className="size-4 text-neutral-400" />
      </div>
      <div className="min-w-0 flex-1">
        <p className="truncate text-sm text-neutral-100">{file.name}</p>
        <div className="mt-1 flex items-center gap-2">
          <span className="rounded-sm bg-neutral-800 px-1 text-[10px] font-medium text-neutral-400">
            {fileType(file)}
          </span>
          <span
            className={`text-xs ${
              failed ? "text-red-400" : complete ? "text-emerald-400" : "text-neutral-400"
            }`}
          >
            {failed ? "Failed to upload" : complete ? "Uploaded to Files" : "Uploading to Files"}
          </span>
        </div>
        {!complete && !failed && (
          <div className="mt-2 h-1 overflow-hidden rounded-full bg-neutral-700">
            <div
              className="h-full rounded-full bg-blue-500 transition-[width] duration-300"
              style={{ width: `${progress}%` }}
            />
          </div>
        )}
      </div>
      <div className="mt-1 shrink-0">
        {complete && (
          <span className="flex size-5 items-center justify-center rounded-full bg-emerald-500/15">
            <Check className="size-3 text-emerald-400" />
          </span>
        )}
        {failed && <AlertCircle className="size-4 text-red-400" />}
      </div>
    </div>
  )
}

function UploadDrawerContent({ userId }: { userId: string }) {
  const [open, setOpen] = useState(false)
  const [minimized, setMinimized] = useState(false)
  const [tab, setTab] = useState<UploadTab>("all")
  const {
    files,
    inputProps,
    openFileDialog,
    isUploading,
    clearAll,
  } = useUppyFileUpload({
    userId,
    maxFiles: 20,
    autoProceed: true,
  })

  useEffect(() => {
    const handleOpenUpload = () => {
      // The picker should be the only UI opened by the button. The drawer is
      // revealed once the hook confirms that an upload has started.
      openFileDialog()
    }

    window.addEventListener(OPEN_UPLOAD_EVENT, handleOpenUpload)
    return () => window.removeEventListener(OPEN_UPLOAD_EVENT, handleOpenUpload)
  }, [openFileDialog])

  const counts = useMemo(
    () => ({
      all: files.length,
      completed: files.filter(isComplete).length,
      failed: files.filter(isFailed).length,
    }),
    [files],
  )
  const inProgress = isUploading || files.some((file) => !isComplete(file) && !isFailed(file))

  useEffect(() => {
    if (isUploading && files.length > 0) {
      setOpen(true)
      setMinimized(false)
      setTab("all")
    }
  }, [files.length, isUploading])

  const filteredFiles = files.filter((file) => {
    if (tab === "completed") return isComplete(file)
    if (tab === "failed") return isFailed(file)
    return true
  })
  const complete = files.length > 0 && !inProgress

  return (
    <>
      <input className="sr-only" type="file" aria-label="Select files to upload" {...inputProps} />
      {open && (
        <aside className="fixed bottom-5 right-5 z-50 w-[min(380px,calc(100vw-2rem))] overflow-hidden rounded-xl border border-neutral-800 bg-neutral-900 text-white shadow-2xl">
          <div className="flex items-center justify-between px-4 py-3">
            <button
              className="flex min-w-0 flex-1 items-center gap-2 text-left"
              onClick={() => setMinimized((value) => !value)}
              type="button"
            >
              <Upload className="size-4 shrink-0 text-neutral-400" />
              <span className="truncate text-sm font-semibold">
                {inProgress
                  ? `Uploading ${counts.all} item${counts.all === 1 ? "" : "s"}…`
                  : "Uploads"}
              </span>
            </button>
            <div className="flex items-center gap-1">
              <Button
                aria-label={minimized ? "Expand uploads" : "Minimize uploads"}
                className="size-7 rounded-md text-neutral-400 hover:bg-neutral-800 hover:text-white"
                onClick={() => setMinimized((value) => !value)}
                size="icon"
                variant="ghost"
              >
                <ChevronDown className={`size-4 transition-transform ${minimized ? "" : "rotate-180"}`} />
              </Button>
              <Button
                aria-label="Close uploads"
                className="size-7 rounded-md text-neutral-400 hover:bg-neutral-800 hover:text-white"
                onClick={() => setOpen(false)}
                size="icon"
                variant="ghost"
              >
                <X className="size-4" />
              </Button>
            </div>
          </div>

          {!minimized && (
            <>
              <div className="flex gap-1 border-y border-neutral-800 px-3 py-2">
                {(["all", "completed", "failed"] as UploadTab[]).map((value) => (
                  <button
                    className={`rounded-full px-3 py-1 text-xs font-medium transition-colors ${
                      tab === value ? "bg-neutral-700 text-white" : "text-neutral-400 hover:bg-neutral-800"
                    }`}
                    key={value}
                    onClick={() => setTab(value)}
                    type="button"
                  >
                    {value === "all" ? "All uploads" : value[0].toUpperCase() + value.slice(1)}
                    {value !== "all" && counts[value] > 0 ? ` (${counts[value]})` : ""}
                  </button>
                ))}
              </div>

              <div className="max-h-64 overflow-y-auto">
                {filteredFiles.length > 0 ? (
                  filteredFiles.map((file) => <FileRow file={file} key={file.id} />)
                ) : (
                  <p className="px-4 py-8 text-center text-xs text-neutral-500">Nothing here yet</p>
                )}
              </div>

              {complete && (
                <div className="flex items-center justify-between border-t border-neutral-800 bg-emerald-500/15 px-4 py-3">
                  <div>
                    <p className="text-sm font-semibold text-emerald-300">Upload successful!</p>
                    <p className="text-xs text-emerald-200/70">
                      {counts.completed} upload{counts.completed === 1 ? "" : "s"} complete
                    </p>
                  </div>
                  <Button
                    className="h-7 rounded-md border border-emerald-400/30 bg-transparent px-2 text-xs text-emerald-200 hover:bg-emerald-400/10"
                    onClick={clearAll}
                    size="sm"
                    variant="outline"
                  >
                    Clear
                  </Button>
                </div>
              )}
            </>
          )}
        </aside>
      )}
    </>
  )
}

export default function UploadDrawer() {
  const { currentUser } = useAuth()

  if (!currentUser) return null
  return <UploadDrawerContent userId={currentUser.userId} />
}

export { OPEN_UPLOAD_EVENT }
