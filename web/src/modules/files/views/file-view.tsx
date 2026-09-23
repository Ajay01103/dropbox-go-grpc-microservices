"use client"

import {
  ChevronRight,
  Download,
  ExternalLink,
  FileText,
  Folder as FolderIcon,
  FolderInput,
  Link as LinkIcon,
  MoreVertical,
  Pencil,
  Share2,
  Star,
  Trash2,
  Users,
} from "lucide-react"
import { useCallback, useMemo, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { useQueryClient } from "@tanstack/react-query"

import type { Item } from "@/gen/pb/metadata/metadata_pb"
import { FileSort } from "@/gen/pb/metadata/metadata_pb"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { cn } from "@/lib/utils"
import { FileFilters } from "@/modules/files/components/file-filters"
import { FileHeader } from "@/modules/files/components/file-header"
import { FilePreviewDrawer } from "@/modules/files/components/file-preview-drawer"
import { InfiniteScrollSentinel } from "@/modules/files/components/infinite-scroll-sentinel"
import {
  useBreadcrumbs,
  useDeleteFile,
  useDeleteFolder,
  useFolderItems,
  useMoveFile,
  useMoveFolder,
  useRenameFolder,
  useThumbnailUrl,
  fileQueryKeys,
} from "@/modules/files/api/use-files"
import { metadataBrowserRpcClient } from "@/lib/rpc"
import { useDraggableItem } from "@/modules/files/dnd/use-draggable-item"
import { useDropTargetFromCurrent } from "@/modules/files/dnd/use-drop-target"
import { useItemMoveMonitor } from "@/modules/files/dnd/use-item-move-monitor"
import type { ItemDragData } from "@/modules/files/dnd/dnd-types"

function ActionMenu({
  itemId,
  name,
  onRename,
  onDelete,
  isPending = false,
}: {
  itemId: string
  name: string
  onRename?: () => void
  onDelete?: () => void
  isPending?: boolean
}) {
  const [isOpen, setIsOpen] = useState(false)

  const close = () => setIsOpen(false)
  const unavailable = (action: string) => {
    close()
    window.alert(`${action} is not available yet.`)
  }

  return (
    // stopPropagation: the checkbox and actions button live inside the card's
    // preview click target, and without this a select/action also opens the
    // preview drawer.
    <div
      className="absolute top-2 right-2 z-30 opacity-0 transition-opacity group-hover:opacity-100 focus-within:opacity-100"
      onClick={(e) => e.stopPropagation()}
    >
      <Button
        aria-expanded={isOpen}
        aria-haspopup="menu"
        aria-label={`Actions for ${name}`}
        className="size-7 rounded-md border bg-background p-0 shadow-sm hover:bg-muted"
        onClick={() => setIsOpen((open) => !open)}
        size="icon"
        type="button"
        variant="outline"
      >
        <MoreVertical className="size-3.5" />
      </Button>
      {isOpen && (
        <div
          className="absolute top-8 right-0 w-48 overflow-hidden rounded-lg border bg-background p-1 text-xs shadow-xl ring-1 ring-black/5"
          role="menu"
        >
          <p className="truncate px-2.5 py-1.5 font-semibold" title={name}>{name}</p>
          <div className="my-1 border-t" />
          <ActionMenuItem icon={Download} onClick={() => unavailable("Download")}>Download</ActionMenuItem>
          <ActionMenuItem icon={ExternalLink} onClick={() => unavailable("Open in")}>Open in</ActionMenuItem>
          <div className="my-1 border-t" />
          <ActionMenuItem icon={LinkIcon} onClick={() => void navigator.clipboard?.writeText(itemId).then(close)}>
            Copy link
          </ActionMenuItem>
          <ActionMenuItem icon={Share2} onClick={() => unavailable("Share")}>Share</ActionMenuItem>
          <ActionMenuItem icon={Users} onClick={() => unavailable("Manage permissions")}>
            Manage permissions
          </ActionMenuItem>
          {(onRename || onDelete) && <div className="my-1 border-t" />}
          {onRename && (
            <ActionMenuItem disabled={isPending} icon={Pencil} onClick={() => { close(); onRename() }}>
              Rename
            </ActionMenuItem>
          )}
          {onDelete && (
            <ActionMenuItem disabled={isPending} icon={Trash2} onClick={() => { close(); onDelete() }}>
              Delete
            </ActionMenuItem>
          )}
        </div>
      )}
    </div>
  )
}

function ActionMenuItem({
  children,
  disabled,
  icon: Icon,
  onClick,
}: {
  children: React.ReactNode
  disabled?: boolean
  icon: typeof Download
  onClick: () => void
}) {
  return (
    <button
      className="flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-left hover:bg-muted disabled:pointer-events-none disabled:opacity-50"
      disabled={disabled}
      onClick={onClick}
      role="menuitem"
      type="button"
    >
      <Icon className="size-4 shrink-0" />
      <span>{children}</span>
    </button>
  )
}

// prefetchFolder warms both the RSC payload and the React Query cache for a
// child folder, so the actual click is served from cache. Fire-and-forget:
// prefetch failures must never surface as UI errors.
function useFolderPrefetch() {
  const router = useRouter()
  const queryClient = useQueryClient()
  return useCallback(
    (folderId: string) => {
      router.prefetch(`/files/${folderId}`)
      void queryClient.prefetchInfiniteQuery({
        queryKey: fileQueryKeys.folderItems(folderId, FileSort.UPDATED_AT),
        queryFn: async ({ pageParam }: { pageParam: string }) => {
          const response = await metadataBrowserRpcClient.listFolderItems({
            folderId,
            pageToken: pageParam,
            pageSize: 50,
            sort: FileSort.UPDATED_AT,
          })
          return { items: response.items, nextPageToken: response.nextPageToken }
        },
        initialPageParam: "",
      })
    },
    [router, queryClient],
  )
}

// BreadcrumbBar is the dedicated path bar rendered as its own row between the
// header and the toolbar — the navigation tree of the folder being viewed.
// It always shows a "My files" home entry first (root route = /files root
// redirect), then every ancestor crumb from GetBreadcrumbs. The current
// folder is emphasized plain text; ancestors are links.
function BreadcrumbBar({ folderId }: { folderId: string }) {
  const { data: crumbs, isLoading } = useBreadcrumbs(folderId)
  const isRoot = !crumbs || crumbs.length <= 1
  return (
    <div className="bg-background/60 border-b px-4 py-2.5 sm:px-6 lg:px-10">
      <nav aria-label="Folder path" className="flex min-w-0 items-center gap-1 text-sm">
        <Link
          className={cn(
            "flex shrink-0 items-center gap-1.5 rounded-md px-2 py-1 transition-colors",
            isRoot ? "bg-muted font-medium text-foreground" : "text-muted-foreground hover:bg-muted hover:text-foreground",
          )}
          href="/files"
        >
          <FolderIcon aria-hidden className="size-4" />
          My files
        </Link>
        {isLoading && (
          <span aria-hidden className="bg-muted ml-1 h-4 w-28 animate-pulse rounded" />
        )}
        {crumbs?.map((folder, index) => {
          const isLast = index === crumbs.length - 1
          const label = folder.name || "My files"
          // The root crumb duplicates the home link — skip it.
          if (index === 0 && !folder.name) return null
          return (
            <span key={folder.folderId} className="flex min-w-0 items-center gap-1">
              <ChevronRight aria-hidden className="text-muted-foreground size-3.5 shrink-0" />
              {isLast ? (
                <span className="bg-muted text-foreground truncate rounded-md px-2 py-1 font-medium" title={label}>
                  {label}
                </span>
              ) : (
                <Link
                  className="text-muted-foreground hover:bg-muted hover:text-foreground truncate rounded-md px-2 py-1 transition-colors"
                  href={`/files/${folder.folderId}`}
                  title={label}
                >
                  {label}
                </Link>
              )}
            </span>
          )
        })}
      </nav>
    </div>
  )
}

function ItemCheckbox({
  checked,
  name,
  onCheckedChange,
}: {
  checked: boolean
  name: string
  onCheckedChange: (checked: boolean) => void
}) {
  return (
    // stopPropagation: the checkbox sits inside the card's click-to-preview
    // target; selecting an item must not open the preview drawer.
    <div
      className="absolute top-2 left-2 z-20"
      onClick={(e) => e.stopPropagation()}
      onKeyDown={(e) => e.stopPropagation()}
    >
      <Checkbox
        aria-label={`Select ${name}`}
        checked={checked}
        className={`size-4 bg-background/95 shadow-sm transition-opacity ${
          checked ? "opacity-100" : "opacity-0 group-hover:opacity-100"
        }`}
        onCheckedChange={(value) => onCheckedChange(value === true)}
      />
    </div>
  )
}

function FolderActions({ folderId, name }: { folderId: string; name: string }) {
  const renameFolder = useRenameFolder()
  const deleteFolder = useDeleteFolder()

  const rename = async () => {
    const nextName = window.prompt("Rename folder", name)?.trim()
    if (!nextName || nextName === name) return
    await renameFolder.mutateAsync({ folderId, newName: nextName })
  }

  const remove = async () => {
    if (!window.confirm(`Delete “${name}”?`)) return
    await deleteFolder.mutateAsync(folderId)
  }

  return (
    <ActionMenu
      itemId={folderId}
      isPending={renameFolder.isPending || deleteFolder.isPending}
      name={name}
      onDelete={() => void remove()}
      onRename={() => void rename()}
    />
  )
}

function FolderCard({
  item,
  isSelected,
  onSelect,
  currentFolderId,
  prefetchFolder,
}: {
  item: Item
  isSelected: boolean
  onSelect: (selected: boolean) => void
  currentFolderId: string
  prefetchFolder: (folderId: string) => void
}) {
  void currentFolderId
  const folder = item.details.case === "folder" ? item.details.value : undefined
  const name = folder?.name ?? "Unnamed folder"
  const router = useRouter()

  const { ref: dragRef, isDragging } = useDraggableItem(
    useMemo<ItemDragData>(
      () => ({
        type: "file-view-item",
        itemType: "folder",
        itemId: item.itemId,
        name,
        fromFolderId: currentFolderId,
      }),
      [item.itemId, name, currentFolderId],
    ),
  )

  const { ref: dropRef, isDragOver } = useDropTargetFromCurrent(
    useCallback(() => ({ type: "file-view-folder", folderId: item.itemId }), [item.itemId]),
  )

  return (
    <article
      className={cn(
        "group min-w-0 rounded-xl transition-opacity",
        isDragging && "opacity-40",
      )}
      ref={(el) => {
        dragRef.current = el
        dropRef.current = el
      }}
    >
      <div
        className={cn(
          "bg-muted/60 relative aspect-square cursor-pointer overflow-visible rounded-xl border p-5 transition-colors hover:border-blue-300",
          isDragOver && "border-lime-500 bg-lime-500/10 ring-2 ring-lime-500",
        )}
        onClick={() => router.push(`/files/${item.itemId}`)}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault()
            router.push(`/files/${item.itemId}`)
          }
        }}
        role="link"
        aria-label={`Open folder ${name}`}
        tabIndex={0}
      >
        <ItemCheckbox checked={isSelected} name={name} onCheckedChange={onSelect} />
        <FolderActions folderId={item.itemId} name={name} />
        <div className="flex h-full items-center justify-center">
          <FolderIcon
            className={cn(
              "size-32 fill-blue-300 text-blue-400 stroke-[1.25] transition-transform sm:size-40",
              isDragOver && "scale-105 fill-lime-300/70 text-lime-600",
            )}
          />
        </div>
        {isDragOver && (
          <div className="absolute inset-x-0 bottom-3 flex justify-center">
            <span className="flex items-center gap-1.5 rounded-full bg-lime-600 px-3 py-1 text-xs font-medium text-white shadow-lg">
              <FolderInput className="size-3.5" />
              Move into “{name}”
            </span>
          </div>
        )}
      </div>
      <div className="mt-4 flex items-start gap-3">
        <FolderIcon className="mt-0.5 size-8 shrink-0 fill-blue-300 text-blue-400 stroke-[1.25]" />
        {/* The name row is the navigation affordance: ID-based route, prefetched
            on hover so opening a child folder feels instant. */}
        <Link
          className="min-w-0 flex-1"
          href={`/files/${item.itemId}`}
          onMouseEnter={() => {
            prefetchFolder(item.itemId)
          }}
          onFocus={() => {
            prefetchFolder(item.itemId)
          }}
        >
          <p className="truncate text-base font-medium hover:underline">{name}</p>
          <p className="text-muted-foreground text-sm">Folder · 1 item</p>
        </Link>
        <button aria-label={`Star ${name}`} className="text-foreground/80 mt-1 shrink-0" type="button">
          <Star className="size-6" />
        </button>
      </div>
    </article>
  )
}

function FileCard({
  item,
  isSelected,
  onSelect,
  currentFolderId,
}: {
  item: Item
  isSelected: boolean
  onSelect: (selected: boolean) => void
  currentFolderId: string
}) {
  const file = item.details.case === "file" ? item.details.value : undefined
  const name = file?.filename ?? "Unnamed file"
  const thumbnail = useThumbnailUrl(item.itemId, file?.thumbnailStatus ?? "", file?.thumbnailKey ?? "")
  const isImage = Boolean(file?.contentType?.startsWith("image/"))
  const router = useRouter()

  // Preview is URL-driven: the intercepted @modal route opens the drawer over
  // the grid while the URL becomes shareable /files/<folderId>/preview/<fileId>.
  const openPreview = useCallback(() => {
    router.push(`/files/${currentFolderId}/preview/${item.itemId}`)
  }, [router, currentFolderId, item.itemId])

  const dragData = useMemo<ItemDragData>(
    () => ({
      type: "file-view-item",
      itemType: "file",
      itemId: item.itemId,
      name,
      fromFolderId: currentFolderId,
    }),
    [item.itemId, name, currentFolderId],
  )
  const { ref: dragRef, isDragging } = useDraggableItem(dragData)

  return (
    <article
      className={cn(
        "group min-w-0 transition-opacity",
        isDragging && "opacity-40",
      )}
      ref={dragRef}
    >
      <div
        className="relative aspect-square cursor-zoom-in overflow-visible rounded-xl border"
        onClick={openPreview}
        role="button"
        aria-label={`Preview ${name}`}
        tabIndex={0}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault()
            openPreview()
          }
        }}
      >
        <div className="bg-muted/40 absolute inset-0 overflow-hidden rounded-xl">
          <ItemCheckbox checked={isSelected} name={name} onCheckedChange={onSelect} />
          {isImage && thumbnail.data ? (
            <img alt={name} className="h-full w-full object-cover" src={thumbnail.data} />
          ) : (
            <div className="flex h-full items-center justify-center">
              <FileText className="text-muted-foreground size-20" />
            </div>
          )}
          <div className="absolute inset-0 bg-black/0 transition-colors group-hover:bg-black/10" />
        </div>
        <ActionMenu itemId={item.itemId} name={name} />
      </div>
      <div className="mt-3 flex items-center gap-2">
        <FileText className="text-muted-foreground size-5 shrink-0" />
        <p className="truncate text-sm font-medium" title={name}>{name}</p>
      </div>
    </article>
  )
}

export const FileView = ({ folderId }: { folderId: string }) => {
  const { data, error, fetchNextPage, hasNextPage, isError, isFetchingNextPage, isLoading } =
    useFolderItems(folderId)
  const items = Array.from(
    new Map(
      (data?.pages.flatMap((page) => page.items) ?? []).map((item) => [
        `${item.itemType}:${item.itemId}`,
        item,
      ]),
    ).values(),
  )
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set())
  const deleteFile = useDeleteFile()
  const deleteFolder = useDeleteFolder()
  const moveFile = useMoveFile()
  const moveFolder = useMoveFolder()
  const prefetchFolder = useFolderPrefetch()
  const selectedItems = items.filter((item) => selectedIds.has(item.itemId))
  const allSelected = items.length > 0 && selectedItems.length === items.length
  const isBusy =
    deleteFile.isPending || deleteFolder.isPending || moveFile.isPending || moveFolder.isPending

  const setItemSelected = (itemId: string, selected: boolean) => {
    setSelectedIds((current) => {
      const next = new Set(current)
      if (selected) next.add(itemId)
      else next.delete(itemId)
      return next
    })
  }

  const toggleAll = (selected: boolean) => {
    setSelectedIds(selected ? new Set(items.map((item) => item.itemId)) : new Set())
  }

  const downloadSelected = () => {
    window.alert("Download is not available yet.")
  }

  const deleteSelected = async () => {
    if (!window.confirm(`Delete ${selectedItems.length} selected item${selectedItems.length === 1 ? "" : "s"}?`)) {
      return
    }

    await Promise.all(
      selectedItems.map((item) =>
        item.details.case === "folder"
          ? deleteFolder.mutateAsync(item.itemId)
          : deleteFile.mutateAsync(item.itemId),
      ),
    )
    setSelectedIds(new Set())
  }

  const moveItem = useCallback(
    ({ itemId, itemType, targetFolderId }: { itemId: string; itemType: "file" | "folder"; targetFolderId: string }) => {
      if (itemType === "folder") {
        void moveFolder.mutateAsync({ itemId, targetFolderId }).catch(() => {
          window.alert("Unable to move the folder — it may create a cycle or the folder may have been deleted.")
        })
        return
      }
      void moveFile.mutateAsync({ itemId, targetFolderId }).catch(() => {
        window.alert("Unable to move the file — it may have been deleted.")
      })
    },
    [moveFile, moveFolder],
  )
  useItemMoveMonitor({ onMoveItem: moveItem })

  const { ref: rootDropRef, isDragOver: isRootDragOver } = useDropTargetFromCurrent(
    useCallback(() => ({ type: "file-view-root" as const }), []),
  )

  return (
    <div
      className={cn(
        "flex min-h-0 flex-1 flex-col overflow-hidden transition-colors",
        isRootDragOver && "bg-lime-50/40 dark:bg-lime-950/20",
      )}
      ref={rootDropRef as React.Ref<HTMLDivElement>}
    >
      <FileHeader folderId={folderId} />
      <BreadcrumbBar folderId={folderId} />
      <FileFilters />
      <section className="min-h-0 flex-1 overflow-auto px-4 py-4 sm:px-6 lg:px-10">
        <div className="flex items-center justify-between gap-3 border-b pb-4">
          <div className="flex min-w-0 items-center gap-3">
            <Checkbox
              aria-label="Select all items"
              checked={allSelected}
              className="size-6 rounded-md"
              disabled={items.length === 0}
              onCheckedChange={(value) => toggleAll(value === true)}
            />
            <span className="text-muted-foreground shrink-0 text-sm">{items.length} items</span>
          </div>
          {selectedItems.length > 0 && (
            <div className="flex shrink-0 items-center gap-2">
              <Button className="h-8 rounded-md px-2.5 text-xs" onClick={downloadSelected} variant="outline">
                <Download className="size-3.5" />
                Download
              </Button>
              <Button
                className="text-destructive hover:text-destructive h-8 rounded-md px-2.5 text-xs"
                disabled={isBusy}
                onClick={() => void deleteSelected()}
                variant="outline"
              >
                <Trash2 className="size-3.5" />
                Delete
              </Button>
            </div>
          )}
        </div>

        {isLoading ? (
          <div className="text-muted-foreground py-12 text-center text-sm">Loading files...</div>
        ) : isError ? (
          <div className="text-destructive py-12 text-center text-sm">
            {error instanceof Error ? error.message : "Unable to load files"}
          </div>
        ) : items.length === 0 ? (
          <div className="text-muted-foreground py-12 text-center text-sm">No files yet</div>
        ) : (
          <div className="mt-6 grid grid-cols-1 gap-x-6 gap-y-8 sm:grid-cols-2 lg:grid-cols-4">
            {items.map((item) =>
              item.details.case === "folder" ? (
                <FolderCard
                  currentFolderId={folderId}
                  isSelected={selectedIds.has(item.itemId)}
                  item={item}
                  key={`${item.itemType}-${item.itemId}`}
                  onSelect={(selected) => setItemSelected(item.itemId, selected)}
                  prefetchFolder={prefetchFolder}
                />
              ) : (
                <FileCard
                  currentFolderId={folderId}
                  isSelected={selectedIds.has(item.itemId)}
                  item={item}
                  key={`${item.itemType}-${item.itemId}`}
                  onSelect={(selected) => setItemSelected(item.itemId, selected)}
                />
              ),
            )}
          </div>
        )}

        {isRootDragOver && (
          <div className="pointer-events-none mt-6 flex justify-center">
            <span className="flex items-center gap-2 rounded-full bg-lime-600 px-4 py-1.5 text-sm font-medium text-white shadow-lg">
              <FolderInput className="size-4" />
              Drop here to move to My files
            </span>
          </div>
        )}

        <InfiniteScrollSentinel
          hasNextPage={Boolean(hasNextPage)}
          isFetchingNextPage={isFetchingNextPage}
          onLoadMore={() => void fetchNextPage()}
        />
        {isFetchingNextPage && (
          <p className="text-muted-foreground py-4 text-center text-sm">Loading more...</p>
        )}
      </section>
    </div>
  )
}
