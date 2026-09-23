"use client"

import { useQueryClient } from "@tanstack/react-query"
import {
  Clock3,
  FileText,
  Folder as FolderIcon,
  Image as ImageIcon,
  RefreshCw,
  Star,
} from "lucide-react"

import type { Item } from "@/gen/pb/metadata/metadata_pb"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { formatBytes, formatRelativeTime } from "@/lib/utils"
import { InfiniteScrollSentinel } from "@/modules/files/components/infinite-scroll-sentinel"
import { fileQueryKeys, useRecentItems, useThumbnailUrl } from "@/modules/files/api/use-files"
import { HomeMainHeader } from "../components/home-main-header"

const RecentFileCard = ({ item }: { item: Item }) => {
  const file = item.details.case === "file" ? item.details.value : undefined
  const name = file?.filename ?? "Unnamed file"
  const thumbnail = useThumbnailUrl(
    item.itemId,
    file?.thumbnailStatus ?? "",
    file?.thumbnailKey ?? "",
  )
  const isImage = Boolean(file?.contentType?.startsWith("image/"))

  return (
    <article className="group min-w-0">
      <div className="relative aspect-square overflow-hidden rounded-xl border shadow-sm transition-all group-hover:shadow-md group-hover:ring-2 group-hover:ring-lime-400/60">
        <div className="bg-muted/40 absolute inset-0">
          {isImage && thumbnail.data ? (
            // eslint-disable-next-line @next/next/no-img-element -- presigned S3 URLs expire, next/image optimization adds no value here
            <img
              alt={name}
              className="h-full w-full object-cover transition-transform duration-300 group-hover:scale-[1.03]"
              src={thumbnail.data}
            />
          ) : (
            <div className="flex h-full items-center justify-center">
              {isImage ? (
                <ImageIcon className="text-muted-foreground/60 size-16" />
              ) : (
                <FileText className="text-muted-foreground/60 size-16" />
              )}
            </div>
          )}
          <div className="absolute inset-0 bg-black/0 transition-colors group-hover:bg-black/5" />
        </div>
      </div>
      <div className="mt-3 min-w-0">
        <p className="truncate text-sm font-medium" title={name}>
          {name}
        </p>
        <p className="text-muted-foreground mt-0.5 truncate text-xs">
          {file?.sizeBytes ? formatBytes(file.sizeBytes) : "File"} ·{" "}
          {formatRelativeTime(file?.createdAt)}
        </p>
      </div>
    </article>
  )
}

const RecentFolderCard = ({ item }: { item: Item }) => {
  const folder = item.details.case === "folder" ? item.details.value : undefined
  const name = folder?.name ?? "Unnamed folder"

  return (
    <article className="group min-w-0">
      <div className="bg-muted/60 relative flex aspect-square items-center justify-center overflow-hidden rounded-xl border p-5 shadow-sm transition-all group-hover:shadow-md group-hover:ring-2 group-hover:ring-blue-400/60">
        <FolderIcon className="size-28 fill-blue-300 text-blue-400 stroke-[1.25] transition-transform duration-300 group-hover:scale-[1.05] sm:size-32" />
      </div>
      <div className="mt-3 flex items-start gap-2.5">
        <FolderIcon className="mt-0.5 size-5 shrink-0 fill-blue-300 text-blue-400 stroke-[1.25]" />
        <div className="min-w-0">
          <p className="truncate text-sm font-medium" title={name}>
            {name}
          </p>
          <p className="text-muted-foreground mt-0.5 truncate text-xs">
            Folder · {formatRelativeTime(folder?.updatedAt || folder?.createdAt)}
          </p>
        </div>
      </div>
    </article>
  )
}

const RecentGrid = ({ items }: { items: Item[] }) => (
  <div className="grid grid-cols-2 gap-x-5 gap-y-7 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5">
    {items.map((item) =>
      item.details.case === "folder" ? (
        <RecentFolderCard item={item} key={`${item.itemType}-${item.itemId}`} />
      ) : (
        <RecentFileCard item={item} key={`${item.itemType}-${item.itemId}`} />
      ),
    )}
  </div>
)

const RecentSkeletons = () => (
  <div className="grid grid-cols-2 gap-x-5 gap-y-7 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5">
    {Array.from({ length: 10 }, (_, i) => (
      <div key={i}>
        <Skeleton className="aspect-square w-full rounded-xl" />
        <Skeleton className="mt-3 h-4 w-3/4" />
        <Skeleton className="mt-2 h-3 w-1/2" />
      </div>
    ))}
  </div>
)

const EmptyState = ({ icon: Icon, title, hint }: { icon: typeof Clock3; title: string; hint: string }) => (
  <div className="text-muted-foreground flex flex-1 flex-col items-center justify-center gap-3 rounded-2xl border border-dashed py-20">
    <div className="bg-muted flex size-14 items-center justify-center rounded-full">
      <Icon className="size-6" />
    </div>
    <p className="text-foreground text-sm font-medium">{title}</p>
    <p className="max-w-xs text-center text-xs">{hint}</p>
  </div>
)

const HomeView = () => {
  const queryClient = useQueryClient()
  const {
    data,
    isError,
    isFetchingNextPage,
    isLoading,
    fetchNextPage,
    hasNextPage,
    refetch,
  } = useRecentItems()
  const items = data?.pages.flatMap((page) => page.items) ?? []

  // useCreateFolder already invalidates the recent-items query; this keeps
  // the header's callback contract without duplicating state in the view.
  const handleFolderCreated = async () => {
    await queryClient.invalidateQueries({ queryKey: fileQueryKeys.recentItems() })
  }

  return (
    <div className="bg-background flex min-h-screen flex-col">
      <HomeMainHeader onFolderCreated={handleFolderCreated} />
      <main className="flex flex-1 flex-col px-4 py-4 sm:px-8 sm:py-6">
        <Tabs defaultValue="recents" className="flex min-h-0 flex-1">
          <TabsList className="bg-muted h-10 rounded-full p-1">
            <TabsTrigger value="recents" className="h-8 rounded-full px-4 text-sm">
              <Clock3 />
              Recents
            </TabsTrigger>
            <TabsTrigger value="starred" className="h-8 rounded-full px-4 text-sm">
              <Star />
              Starred
            </TabsTrigger>
          </TabsList>
          <TabsContent value="recents" className="mt-5 flex min-h-0 flex-1 flex-col">
            {isLoading ? (
              <RecentSkeletons />
            ) : isError ? (
              <div className="text-destructive flex flex-1 flex-col items-center justify-center gap-3 rounded-2xl border border-dashed py-20">
                <p className="text-sm font-medium">Unable to load recent items</p>
                <Button className="h-8 rounded-lg px-3 text-xs" onClick={() => void refetch()} variant="outline">
                  <RefreshCw />
                  Try again
                </Button>
              </div>
            ) : items.length > 0 ? (
              <div className="min-h-0 flex-1 overflow-auto pb-6">
                <div className="mb-4 flex items-baseline justify-between border-b pb-3">
                  <h2 className="text-base font-semibold">Recent</h2>
                  <span className="text-muted-foreground text-sm">
                    {items.length} item{items.length === 1 ? "" : "s"}
                  </span>
                </div>
                <RecentGrid items={items} />
                <InfiniteScrollSentinel
                  hasNextPage={Boolean(hasNextPage)}
                  isFetchingNextPage={isFetchingNextPage}
                  onLoadMore={() => void fetchNextPage()}
                />
                {isFetchingNextPage && (
                  <p className="text-muted-foreground py-4 text-center text-sm">Loading more…</p>
                )}
              </div>
            ) : (
              <EmptyState
                hint="Files and folders you create, upload, or open will show up here."
                icon={Clock3}
                title="No recent items yet"
              />
            )}
          </TabsContent>
          <TabsContent value="starred" className="mt-5 flex min-h-0 flex-1 flex-col">
            <EmptyState
              hint="Star files and folders to pin them here for quick access."
              icon={Star}
              title="Nothing starred yet"
            />
          </TabsContent>
        </Tabs>
      </main>
    </div>
  )
}

export default HomeView
