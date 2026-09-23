"use client"

import { ArchiveRestore, Folder, ImageIcon, Trash2 } from "lucide-react"
import { useMemo, useState } from "react"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import {
  usePermanentlyDeleteFile,
  useRestoreFile,
  useRestoreFolder,
  useTrashItems,
} from "@/modules/files/api/use-files"

type DeletedItem = {
  id: string
  name: string
  deletedAt: string
  kind: "file" | "folder"
}

function formatDeletedAt(value: string) {
  if (!value) return "—"

  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value

  return date.toLocaleString("en-US", {
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
    month: "numeric",
    year: "numeric",
  })
}

function itemKey(item: DeletedItem) {
  return `${item.kind}:${item.id}`
}

export const DeletedFilesView = () => {
  const { data, error, isError, isLoading } = useTrashItems()
  const [selectedKeys, setSelectedKeys] = useState<Set<string>>(new Set())
  const restoreFile = useRestoreFile()
  const restoreFolder = useRestoreFolder()
  const permanentlyDeleteFile = usePermanentlyDeleteFile()

  const items = useMemo<DeletedItem[]>(
    () =>
      data?.pages.flatMap((page) => [
        ...page.files.map((file) => ({
          id: file.fileId,
          name: file.filename || "Unnamed file",
          deletedAt: file.deletedAt,
          kind: "file" as const,
        })),
        ...page.folders.map((folder) => ({
          id: folder.folderId,
          name: folder.name || "Unnamed folder",
          deletedAt: folder.deletedAt,
          kind: "folder" as const,
        })),
      ]) ?? [],
    [data],
  )

  const selectedItems = items.filter((item) => selectedKeys.has(itemKey(item)))
  const allSelected = items.length > 0 && selectedItems.length === items.length
  const isMutating =
    restoreFile.isPending || restoreFolder.isPending || permanentlyDeleteFile.isPending

  const setSelected = (item: DeletedItem, selected: boolean) => {
    setSelectedKeys((current) => {
      const next = new Set(current)
      if (selected) next.add(itemKey(item))
      else next.delete(itemKey(item))
      return next
    })
  }

  const toggleAll = (selected: boolean) => {
    setSelectedKeys(selected ? new Set(items.map(itemKey)) : new Set())
  }

  const restoreSelected = async () => {
    await Promise.all(
      selectedItems.map((item) =>
        item.kind === "file"
          ? restoreFile.mutateAsync(item.id)
          : restoreFolder.mutateAsync(item.id),
      ),
    )
    setSelectedKeys(new Set())
  }

  const permanentlyDeleteSelected = async () => {
    const folderSelected = selectedItems.some((item) => item.kind === "folder")
    if (folderSelected) {
      window.alert("Permanent folder deletion is not available yet.")
    }

    const files = selectedItems.filter((item) => item.kind === "file")
    if (files.length === 0) return
    if (!window.confirm(`Permanently delete ${files.length} selected file${files.length === 1 ? "" : "s"}?`)) {
      return
    }

    await Promise.all(files.map((item) => permanentlyDeleteFile.mutateAsync(item.id)))
    setSelectedKeys(new Set())
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-auto bg-background px-6 py-6 sm:px-10 sm:py-8">
      <div className="max-w-none">
        <h1 className="text-3xl font-normal tracking-tight">Deleted files</h1>
        <p className="mt-8 text-sm text-foreground">
          Restore deleted files. Files are permanently deleted after 30 days. {" "}
        </p>
      </div>

      <div className="mt-12 border-t pt-10">
        {selectedItems.length > 0 && (
          <div className="mb-8 flex items-center gap-4">
            <span className="text-base font-semibold">{selectedItems.length} selected:</span>
            <Button className="h-10 rounded-lg px-3" disabled={isMutating} onClick={() => void restoreSelected()} variant="outline">
              <ArchiveRestore className="size-5" />
              Restore
            </Button>
            <Button
              className="h-10 rounded-lg px-3"
              disabled={isMutating}
              onClick={() => void permanentlyDeleteSelected()}
              variant="outline"
            >
              <Trash2 className="size-5" />
              Delete permanently
            </Button>
          </div>
        )}

        {isLoading ? (
          <p className="text-muted-foreground py-12 text-center text-sm">Loading deleted files...</p>
        ) : isError ? (
          <p className="text-destructive py-12 text-center text-sm">
            {error instanceof Error ? error.message : "Unable to load deleted files"}
          </p>
        ) : (
          <Table className="text-base">
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="w-16 px-5">
                  <Checkbox
                    aria-label="Select all deleted files"
                    checked={allSelected}
                    onCheckedChange={(value) => toggleAll(value === true)}
                  />
                </TableHead>
                <TableHead className="h-12 text-base font-normal text-muted-foreground">Name</TableHead>
                <TableHead className="h-12 text-base font-normal text-muted-foreground">Deleted by</TableHead>
                <TableHead className="h-12 text-base font-normal text-muted-foreground">Date deleted</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((item) => {
                const selected = selectedKeys.has(itemKey(item))
                return (
                  <TableRow className={selected ? "bg-blue-50 hover:bg-blue-50" : "h-18"} key={itemKey(item)}>
                    <TableCell className="w-16 px-5">
                      <Checkbox
                        aria-label={`Select ${item.name}`}
                        checked={selected}
                        onCheckedChange={(value) => setSelected(item, value === true)}
                      />
                    </TableCell>
                    <TableCell className="max-w-[min(48vw,620px)] font-semibold">
                      <div className="flex min-w-0 items-center gap-5">
                        {item.kind === "folder" ? (
                          <Folder className="size-10 shrink-0 fill-blue-300 text-blue-400 stroke-[1.25]" />
                        ) : (
                          <span className="flex size-8 shrink-0 items-center justify-center rounded border bg-stone-100 text-stone-500">
                            <ImageIcon className="size-5" />
                          </span>
                        )}
                        <span className="truncate" title={item.name}>{item.name}</span>
                      </div>
                    </TableCell>
                    <TableCell>You</TableCell>
                    <TableCell>{formatDeletedAt(item.deletedAt)}</TableCell>
                  </TableRow>
                )
              })}
              {items.length === 0 && (
                <TableRow>
                  <TableCell className="text-muted-foreground py-16 text-center" colSpan={4}>
                    No deleted files
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        )}
      </div>
    </div>
  )
}
