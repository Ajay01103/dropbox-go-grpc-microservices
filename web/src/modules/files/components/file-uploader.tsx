"use client"

import type { Body, Meta, UppyFile } from "@uppy/core"
import {
  AlertCircle,
  FileArchive,
  File as FileIcon,
  FileSpreadsheet,
  FileText,
  Headphones,
  Image as ImageIcon,
  Trash2,
  Upload,
  Video,
  X,
} from "lucide-react"
import { Button } from "@/components/ui/button" // swap for your own button if not on shadcn/ui
import { useUppyFileUpload } from "../hooks/use-uppy-file-upload"

const MAX_FILES = 6

function formatBytes(bytes: number, decimals = 1) {
  if (bytes === 0) return "0 Bytes"
  const k = 1024
  const sizes = ["Bytes", "KB", "MB", "GB"]
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(decimals))} ${sizes[i]}`
}

const iconMap: Array<{ icon: typeof FileText; test: (type: string, name: string) => boolean }> = [
  {
    icon: FileText,
    test: (type, name) =>
      type.includes("pdf") ||
      name.endsWith(".pdf") ||
      type.includes("word") ||
      /\.(docx?)$/i.test(name),
  },
  {
    icon: FileArchive,
    test: (type, name) =>
      type.includes("zip") || type.includes("archive") || /\.(zip|rar)$/i.test(name),
  },
  {
    icon: FileSpreadsheet,
    test: (type, name) => type.includes("excel") || /\.xlsx?$/i.test(name),
  },
  { icon: Video, test: (type) => type.startsWith("video/") },
  { icon: Headphones, test: (type) => type.startsWith("audio/") },
  { icon: ImageIcon, test: (type) => type.startsWith("image/") },
]

function getFileIcon(file: UppyFile<Meta, Body>) {
  const type = file.type ?? ""
  const name = file.name ?? ""
  return iconMap.find((m) => m.test(type, name))?.icon ?? FileIcon
}

export function FileUploader({ userId }: { userId: string }) {
  const {
    files,
    errors,
    isDragging,
    dragHandlers,
    inputProps,
    openFileDialog,
    uploadFiles,
    isUploading,
    removeFile,
    clearAll,
  } = useUppyFileUpload({ userId, maxFiles: MAX_FILES })

  return (
    <div className="flex flex-col gap-2">
      {/* Drop area */}
      <div
        className="border-input data-[dragging=true]:bg-accent/50 relative flex min-h-52 flex-col items-center overflow-hidden rounded-xl border border-dashed p-4 transition-colors not-data-files:justify-center"
        data-dragging={isDragging || undefined}
        data-files={files.length > 0 || undefined}
        {...dragHandlers}
      >
        <input
          className="sr-only"
          type="file"
          aria-label="Upload files"
          {...inputProps}
        />

        {files.length > 0 ? (
          <div className="flex w-full flex-col gap-3">
            <div className="flex items-center justify-between gap-2">
              <h3 className="truncate text-sm font-medium">Files ({files.length})</h3>
              <div className="flex gap-2">
                <Button
                  onClick={uploadFiles}
                  disabled={isUploading}
                  size="sm"
                >
                  <Upload
                    className="-ms-0.5 size-3.5 opacity-60"
                    aria-hidden="true"
                  />
                  {isUploading ? "Uploading..." : "Upload files"}
                </Button>
                <Button
                  onClick={openFileDialog}
                  variant="outline"
                  size="sm"
                  disabled={isUploading}
                >
                  <Upload
                    className="-ms-0.5 size-3.5 opacity-60"
                    aria-hidden="true"
                  />
                  Add files
                </Button>
                <Button
                  onClick={clearAll}
                  variant="outline"
                  size="sm"
                >
                  <Trash2
                    className="-ms-0.5 size-3.5 opacity-60"
                    aria-hidden="true"
                  />
                  Remove all
                </Button>
              </div>
            </div>

            <div className="w-full space-y-2">
              {files.map((file) => {
                const Icon = getFileIcon(file)
                const progress = file.progress?.percentage ?? 0
                const uploading =
                  !file.progress?.uploadComplete && (file.progress?.uploadStarted ?? false)

                return (
                  <div
                    key={file.id}
                    className="bg-background flex flex-col gap-1 rounded-lg border p-2 pe-3 transition-opacity duration-300"
                    data-uploading={uploading || undefined}
                  >
                    <div className="flex items-center justify-between gap-2">
                      <div className="flex items-center gap-3 overflow-hidden in-data-[uploading=true]:opacity-50">
                        <div className="flex aspect-square size-10 shrink-0 items-center justify-center rounded border">
                          <Icon className="size-4" />
                        </div>
                        <div className="flex min-w-0 flex-col gap-0.5">
                          <p className="truncate text-[13px] font-medium">{file.name}</p>
                          <p className="text-muted-foreground text-xs">
                            {formatBytes(file.size ?? 0)}
                          </p>
                        </div>
                      </div>
                      <Button
                        className="text-muted-foreground/80 hover:text-foreground -me-2 size-8 hover:bg-transparent"
                        onClick={() => removeFile(file.id)}
                        size="icon"
                        variant="ghost"
                        aria-label="Remove file"
                      >
                        <X
                          className="size-4"
                          aria-hidden="true"
                        />
                      </Button>
                    </div>

                    {uploading && (
                      <div className="mt-1 flex items-center gap-2">
                        <div className="h-1.5 w-full overflow-hidden rounded-full bg-gray-100">
                          <div
                            className="bg-primary h-full transition-all duration-300 ease-out"
                            style={{ width: `${progress}%` }}
                          />
                        </div>
                        <span className="text-muted-foreground w-10 text-xs tabular-nums">
                          {progress}%
                        </span>
                      </div>
                    )}
                  </div>
                )
              })}
            </div>
          </div>
        ) : (
          <div className="flex flex-col items-center justify-center px-4 py-3 text-center">
            <div
              className="bg-background mb-2 flex size-11 shrink-0 items-center justify-center rounded-full border"
              aria-hidden="true"
            >
              <ImageIcon className="size-4 opacity-60" />
            </div>
            <p className="mb-1.5 text-sm font-medium">Drop your files here</p>
            <p className="text-muted-foreground text-xs">
              Max {MAX_FILES} files &middot; Chunked upload for large files
            </p>
            <Button
              className="mt-4"
              onClick={openFileDialog}
              variant="outline"
            >
              <Upload
                className="-ms-1 opacity-60"
                aria-hidden="true"
              />
              Select files
            </Button>
          </div>
        )}
      </div>

      {errors.length > 0 && (
        <div
          className="text-destructive flex items-center gap-1 text-xs"
          role="alert"
        >
          <AlertCircle className="size-3 shrink-0" />
          <span>{errors[0]}</span>
        </div>
      )}
    </div>
  )
}
