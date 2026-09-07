"use client"

import { useEffect, useMemo, useRef, useState, useCallback } from "react"
import Uppy, { type UppyFile } from "@uppy/core"
import type { Body, Meta } from "@uppy/core"
import GoldenRetriever from "@uppy/golden-retriever"
import { useUppyState, useUppyEvent } from "@uppy/react"
import { uploadBrowserRpcClient } from "@/lib/rpc"
import { ConnectRPCUploader } from "@/lib/uppy-connectrpc-uploader"

interface UseUppyFileUploadOpts {
  userId: string
  maxFiles?: number
  maxSizeMB?: number
  accept?: string // e.g. 'image/*'
}

export function useUppyFileUpload({
  userId,
  maxFiles = 6,
  maxSizeMB,
  accept,
}: UseUppyFileUploadOpts) {
  const inputRef = useRef<HTMLInputElement>(null)
  const mountedRef = useRef(false)
  const [isDragging, setIsDragging] = useState(false)
  const [errors, setErrors] = useState<string[]>([])
  const [isUploading, setIsUploading] = useState(false)

  // Lazy-init: never construct Uppy at module scope or during SSR render.
  // useState's initializer form runs once, on the client, on first render.
  const [uppy] = useState(() => {
    const instance = new Uppy<Meta, Body>({
      autoProceed: false,
      restrictions: {
        maxNumberOfFiles: maxFiles,
        ...(maxSizeMB !== undefined && { maxFileSize: maxSizeMB * 1024 * 1024 }),
        allowedFileTypes: accept ? [accept] : undefined,
      },
    })

    instance.use(GoldenRetriever) // resumes across reload/crash via IndexedDB
    instance.use(ConnectRPCUploader, {
      id: "ConnectRPCUploader",
      client: uploadBrowserRpcClient,
      userId,
    })

    return instance
  })

  useEffect(() => {
    mountedRef.current = true

    return () => {
      mountedRef.current = false
      queueMicrotask(() => {
        if (!mountedRef.current) uppy.destroy()
      })
    }
  }, [uppy, mountedRef])

  // Reactive slice of Uppy's internal store — re-renders whenever files change.
  const filesById = useUppyState(uppy, (state) => state.files)
  const files = useMemo(() => Object.values(filesById) as UppyFile<Meta, Body>[], [filesById])

  const pushError = useCallback((message: string) => {
    setErrors((prev) => [message, ...prev].slice(0, 1))
  }, [])

  useUppyEvent(uppy, "restriction-failed", (_file, error) => pushError(error.message))
  useUppyEvent(uppy, "upload-error", (_file, error) => pushError(error.message))
  useUppyEvent(uppy, "upload-success", () => setErrors([]))

  const addFiles = useCallback(
    (fileList: FileList | File[] | null) => {
      if (!fileList) return
      Array.from(fileList).forEach((file) => {
        try {
          uppy.addFile({ name: file.name, type: file.type, data: file, source: "Local" })
        } catch {
          // Uppy already emitted 'restriction-failed' for this file.
        }
      })
    },
    [uppy],
  )

  const openFileDialog = useCallback(() => inputRef.current?.click(), [])
  const uploadFiles = useCallback(async () => {
    if (isUploading) return
    if (files.length === 0) {
      pushError("Select at least one file before uploading")
      return
    }

    setErrors([])
    setIsUploading(true)
    console.info("[files] upload started", { fileCount: files.length })
    try {
      const result = await uppy.upload()
      console.info("[files] upload completed", result)
    } catch (error) {
      const message = error instanceof Error ? error.message : "Upload failed"
      console.error("[files] upload failed", error)
      pushError(message)
    } finally {
      setIsUploading(false)
    }
  }, [files.length, isUploading, pushError, uppy])
  const removeFile = useCallback((fileId: string) => uppy.removeFile(fileId), [uppy])
  const clearAll = useCallback(() => {
    uppy.cancelAll()
    setErrors([])
  }, [uppy])

  const dragHandlers = {
    onDragEnter: (e: React.DragEvent) => {
      e.preventDefault()
      setIsDragging(true)
    },
    onDragOver: (e: React.DragEvent) => {
      e.preventDefault()
    },
    onDragLeave: (e: React.DragEvent) => {
      e.preventDefault()
      setIsDragging(false)
    },
    onDrop: (e: React.DragEvent) => {
      e.preventDefault()
      setIsDragging(false)
      addFiles(e.dataTransfer.files)
    },
  }

  const inputProps = {
    ref: inputRef,
    accept,
    multiple: maxFiles > 1,
    onChange: (e: React.ChangeEvent<HTMLInputElement>) => {
      addFiles(e.target.files)
      e.target.value = "" // allow re-selecting the same file
    },
  }

  return {
    uppy,
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
  }
}
