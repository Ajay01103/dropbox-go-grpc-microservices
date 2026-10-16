"use client"

import * as React from "react"
import dynamic from "next/dynamic"
import { useRouter } from "next/navigation"
import { useQueryClient } from "@tanstack/react-query"
import { AlertTriangle, ArrowLeft, Check, Loader2, Save, X } from "lucide-react"

import { Button } from "@/components/ui/button"
import type { DocxEditorSaveHandle } from "@/components/extend/docx-editor"
import type { PDFEditorHandle } from "@/components/extend/pdf-editor"
import type { XlsxEditorSaveHandle } from "@/components/extend/xlsx-editor"
import { fileQueryKeys } from "@/modules/files/api/use-files"
import { useEditSession, useFileForEdit } from "@/modules/files/api/use-edit-session"
import { DocumentLoadingState } from "@/modules/files/components/document-preview-shell"
import {
  EditSaveAbortedError,
  saveEditedFile,
  type EditSaveProgress,
} from "@/modules/files/lib/edit-commit"
import { cn, formatBytes } from "@/lib/utils"

// Every editor is a WASM document engine that touches window, so each is
// imported only for the type actually being edited and never during SSR.
const PDFEditor = dynamic(
  () => import("@/components/extend/pdf-editor").then((module) => module.PDFEditor),
  { ssr: false, loading: () => <DocumentLoadingState label="Loading PDF editor" /> },
)
const DocxEditorPreview = dynamic(
  () =>
    import("@/components/extend/docx-editor").then((module) => module.DocxEditorPreview),
  { ssr: false, loading: () => <DocumentLoadingState label="Loading document editor" /> },
)
const XlsxEditorPreview = dynamic(
  () =>
    import("@/components/extend/xlsx-editor").then((module) => module.XlsxEditorPreview),
  { ssr: false, loading: () => <DocumentLoadingState label="Loading workbook editor" /> },
)

/** The editors that record which editor produced a version, for audit. */
const EDITOR_NAMES: Record<string, string> = {
  pdf: "extend-pdf-editor",
  docx: "extend-docx-editor",
  xlsx: "extend-xlsx-editor",
}

type SaveState =
  | { status: "idle" }
  | { status: "saving"; progress: EditSaveProgress }
  | { status: "saved"; version: number; bytesUploaded: number }
  | { status: "conflict"; headVersion: number }
  | { status: "failed"; message: string }

/**
 * FileEditView is the edit route's body: it loads a file's current version into
 * the right editor and saves edits as a new version.
 *
 * The file never passes through the app's own API: the editor reads the
 * version-pinned ticket URL directly, and a save hashes the edited bytes in the
 * browser, sends only the blocks the file does not already hold, and commits.
 * A save therefore keeps the previous version intact, and a save that loses a
 * race is reported as a conflict instead of overwriting whoever won.
 */
export function FileEditView({ fileId, folderId }: { fileId: string; folderId: string }) {
  const router = useRouter()
  const queryClient = useQueryClient()
  const fileQuery = useFileForEdit(fileId)
  const sessionQuery = useEditSession(fileId)
  const session = sessionQuery.data
  const file = fileQuery.data

  const pdfRef = React.useRef<PDFEditorHandle | null>(null)
  const docxRef = React.useRef<DocxEditorSaveHandle | null>(null)
  const xlsxRef = React.useRef<XlsxEditorSaveHandle | null>(null)
  const abortRef = React.useRef<AbortController | null>(null)
  const [saveState, setSaveState] = React.useState<SaveState>({ status: "idle" })
  const [isDirty, setIsDirty] = React.useState(false)

  const canEdit = Boolean(session?.canEdit)
  const editorType = session?.sniffedType ?? ""

  // Unsaved-changes guard: a browser refresh or a closed tab is the one exit
  // the in-app guard below cannot intercept.
  React.useEffect(() => {
    if (!isDirty) return
    const onBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault()
    }
    window.addEventListener("beforeunload", onBeforeUnload)
    return () => window.removeEventListener("beforeunload", onBeforeUnload)
  }, [isDirty])

  const collectEditedBytes = React.useCallback(async (): Promise<Uint8Array> => {
    if (editorType === "pdf") {
      const buffer = await pdfRef.current?.getDocumentBuffer()
      if (!buffer) throw new Error("The PDF editor is still loading.")
      return new Uint8Array(buffer)
    }
    const handle = editorType === "docx" ? docxRef.current : xlsxRef.current
    if (!handle) throw new Error("The editor is still loading.")
    return handle.getDocumentBytes()
  }, [editorType])

  const handleSave = React.useCallback(async () => {
    if (!session || saveState.status === "saving") return
    const controller = new AbortController()
    abortRef.current = controller
    setSaveState({
      status: "saving",
      progress: { phase: "hashing", completed: 0, total: 0, bytesUploaded: 0, bytesToUpload: 0 },
    })
    try {
      const bytes = await collectEditedBytes()
      const result = await saveEditedFile({
        fileId,
        baseVersion: session.version,
        blockSize: session.blockSize,
        filename: session.filename,
        editor: EDITOR_NAMES[editorType] ?? "extend-editor",
        bytes,
        signal: controller.signal,
        onProgress: (progress) => setSaveState({ status: "saving", progress }),
      })
      if (result.status === "conflict") {
        setSaveState({ status: "conflict", headVersion: result.headVersion })
        // The base is gone: the editor is now showing bytes the file no longer
        // has, so the session is refetched before the user can try again.
        await sessionQuery.refetch()
        return
      }
      setIsDirty(false)
      setSaveState({ status: "saved", version: result.version, bytesUploaded: result.bytesUploaded })
      await Promise.all([
        sessionQuery.refetch(),
        fileQuery.refetch(),
        // The bytes changed, so the ticket for the old version and every list
        // that shows the file's size or thumbnail are stale.
        queryClient.invalidateQueries({ queryKey: ["content-url", fileId] }),
        queryClient.invalidateQueries({ queryKey: ["thumbnail-urls"] }),
        queryClient.invalidateQueries({ queryKey: fileQueryKeys.all }),
      ])
    } catch (error) {
      if (error instanceof EditSaveAbortedError) {
        setSaveState({ status: "idle" })
        return
      }
      setSaveState({
        status: "failed",
        message: error instanceof Error ? error.message : "The save failed.",
      })
    } finally {
      abortRef.current = null
    }
  }, [collectEditedBytes, editorType, fileId, fileQuery, queryClient, saveState.status, session, sessionQuery])

  const handleCancelSave = React.useCallback(() => {
    abortRef.current?.abort()
  }, [])

  const leaveEdit = React.useCallback(() => {
    if (isDirty && !window.confirm("Discard your unsaved changes?")) return
    router.push(`/files/${folderId}/preview/${fileId}`)
  }, [fileId, folderId, isDirty, router])

  // docx and xlsx expose no change event, so any typing or pointer input that
  // bubbles out of the editor is treated as an edit. The guard is advisory, and
  // a false positive only asks before discarding.
  const trackInteraction = React.useCallback(() => setIsDirty(true), [])

  if (fileQuery.isError || sessionQuery.isError) {
    return (
      <EditMessage
        icon={<AlertTriangle className="size-10" strokeWidth={1.5} />}
        message="This file could not be opened for editing."
        title="Unavailable"
      >
        <Button onClick={() => router.push(`/files/${folderId}`)} size="sm" variant="outline">
          Back to files
        </Button>
      </EditMessage>
    )
  }

  if (!session || !file) {
    return <DocumentLoadingState label="Opening the editor" />
  }

  const name = session.filename || file.filename

  return (
    <div className="bg-background flex h-full min-h-0 flex-col">
      <header className="flex flex-wrap items-center gap-3 border-b px-4 py-3">
        <Button
          aria-label="Back to preview"
          className="text-muted-foreground shrink-0 gap-1.5"
          onClick={leaveEdit}
          size="sm"
          variant="ghost"
        >
          <ArrowLeft className="h-4 w-4" />
          Back
        </Button>
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-semibold" title={name}>
            {name}
          </p>
          <p className="text-muted-foreground truncate text-xs">
            {[
              editorType.toUpperCase(),
              formatBytes(Number(file.sizeBytes)),
              `version ${session.version}`,
            ]
              .filter(Boolean)
              .join(" · ")}
          </p>
        </div>
        <SaveStateIndicator state={saveState} />
        {saveState.status === "saving" ? (
          <Button
            className="shrink-0 gap-1.5"
            onClick={handleCancelSave}
            size="sm"
            variant="outline"
          >
            <X className="h-4 w-4" />
            Cancel
          </Button>
        ) : (
          <Button
            className="shrink-0 gap-1.5"
            disabled={!canEdit}
            onClick={() => void handleSave()}
            size="sm"
          >
            <Save className="h-4 w-4" />
            Save as new version
          </Button>
        )}
      </header>

      {saveState.status === "conflict" ? (
        <EditNotice tone="warning">
          A newer version of this file exists (version {saveState.headVersion}). Your changes
          were not saved. Save your edited copy with the editor's own download, then reopen
          the file to edit its latest version.
        </EditNotice>
      ) : null}
      {saveState.status === "failed" ? (
        <EditNotice tone="error">{saveState.message}</EditNotice>
      ) : null}
      {saveState.status === "saved" ? (
        <EditNotice tone="success">
          Saved as version {saveState.version} ({formatBytes(saveState.bytesUploaded)}{" "}
          uploaded). The previous version is kept.
        </EditNotice>
      ) : null}
      {!canEdit ? (
        <EditNotice tone="warning">{unavailableMessage(session)}</EditNotice>
      ) : null}

      <div
        className="min-h-0 flex-1 overflow-hidden"
        onInput={trackInteraction}
        onKeyDown={trackInteraction}
        onPointerUp={trackInteraction}
      >
        {!canEdit ? null : editorType === "pdf" ? (
          <PDFEditor
            ref={pdfRef}
            className="h-full"
            defaultMode="view"
            fileName={name}
            // The replacement-upload path bypasses the block store and the
            // version history, so it is off: this editor saves through commit.
            showUpload={false}
            src={session.ticketUrl}
            onAnnotationsChange={trackInteraction}
            onFormValuesChange={trackInteraction}
            onRedactionsApplied={trackInteraction}
          />
        ) : editorType === "docx" ? (
          <DocxEditorPreview
            ref={docxRef}
            className="h-full"
            fileName={name}
            isDark={false}
            src={session.ticketUrl}
            onIsDarkChange={() => undefined}
          />
        ) : (
          <XlsxEditorPreview
            ref={xlsxRef}
            className="h-full"
            fileName={name}
            isDark={false}
            src={session.ticketUrl}
            onIsDarkChange={() => undefined}
          />
        )}
      </div>
    </div>
  )
}

function unavailableMessage(session: {
  unavailableReason: string
  sniffedType: string
  sizeBytes: number
  maxEditBytes: number
}): string {
  switch (session.unavailableReason) {
    case "too_large":
      return `This file is ${formatBytes(session.sizeBytes)}; editing is limited to ${formatBytes(
        session.maxEditBytes,
      )}. Open it in a desktop app instead.`
    case "disabled":
      return "Editing is temporarily switched off for this file type."
    default:
      return "This file type cannot be edited in the browser yet. PDF, DOCX and XLSX are supported."
  }
}

function SaveStateIndicator({ state }: { state: SaveState }) {
  if (state.status === "saving") {
    const { progress } = state
    const label =
      progress.phase === "hashing"
        ? "Preparing changes"
        : progress.phase === "uploading"
          ? `Uploading ${progress.completed}/${progress.total} blocks`
          : "Committing version"
    return (
      <span className="text-muted-foreground flex items-center gap-2 text-xs">
        <Loader2 className="h-3.5 w-3.5 animate-spin" />
        {label}
      </span>
    )
  }
  if (state.status === "saved") {
    return (
      <span className="flex items-center gap-1.5 text-xs text-emerald-600">
        <Check className="h-3.5 w-3.5" />
        Saved as version {state.version}
      </span>
    )
  }
  return null
}

function EditNotice({
  children,
  tone,
}: {
  children: React.ReactNode
  tone: "success" | "warning" | "error"
}) {
  return (
    <div
      className={cn(
        "border-b px-4 py-2 text-xs",
        tone === "success" && "bg-emerald-50 text-emerald-800",
        tone === "warning" && "bg-amber-50 text-amber-900",
        tone === "error" && "bg-destructive/10 text-destructive",
      )}
      role="status"
    >
      {children}
    </div>
  )
}

function EditMessage({
  children,
  icon,
  message,
  title,
}: {
  children?: React.ReactNode
  icon: React.ReactNode
  message: string
  title: string
}) {
  return (
    <div className="text-muted-foreground flex h-full w-full flex-col items-center justify-center gap-3 p-8 text-center">
      {icon}
      <p className="text-foreground text-sm font-medium">{title}</p>
      <p className="max-w-md text-sm">{message}</p>
      {children}
    </div>
  )
}
