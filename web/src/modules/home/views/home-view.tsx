"use client"

import { useQuery } from "@tanstack/react-query"
import { Clock3, Folder as FolderIcon, Star } from "lucide-react"

import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import type { Folder } from "@/gen/pb/metadata/metadata_pb"
import { metadataBrowserRpcClient } from "@/lib/rpc"
import { HomeMainHeader } from "../components/home-main-header"

const recentFoldersQueryKey = ["home", "recent-folders"]

const HomeView = () => {
  const {
    data: folders = [],
    isError,
    isLoading,
    refetch,
  } = useQuery<Folder[]>({
    queryKey: recentFoldersQueryKey,
    queryFn: async () => {
      const response = await metadataBrowserRpcClient.listFolderContents({
        folderId: "",
        pageSize: 100,
      })

      return [...response.folders].sort((a, b) => {
        return (b.createdAt || "").localeCompare(a.createdAt || "")
      })
    },
  })

  const handleFolderCreated = async () => {
    // The create response is not used as optimistic UI. Read the root again
    // so Recents only shows folders persisted by the metadata service.
    await refetch()
  }

  return (
    <div className="flex min-h-screen flex-col bg-background">
      <HomeMainHeader onFolderCreated={handleFolderCreated} />
      <main className="flex flex-1 flex-col px-4 py-4 sm:px-8 sm:py-6">
        <Tabs defaultValue="recents" className="flex min-h-0 flex-1">
          <TabsList className="h-10 rounded-full bg-muted p-1">
            <TabsTrigger value="recents" className="h-8 rounded-full px-4 text-sm">
              <Clock3 />
              Recents
            </TabsTrigger>
            <TabsTrigger value="starred" className="h-8 rounded-full px-4 text-sm">
              <Star />
              Starred
            </TabsTrigger>
          </TabsList>
          <TabsContent value="recents" className="mt-4 flex min-h-0 flex-1 flex-col">
            {isLoading ? (
              <div className="flex flex-1 items-center justify-center rounded-2xl border border-dashed text-sm text-muted-foreground">
                Loading recent folders...
              </div>
            ) : isError ? (
              <div className="flex flex-1 items-center justify-center rounded-2xl border border-dashed text-sm text-destructive">
                Unable to load recent folders
              </div>
            ) : folders.length > 0 ? (
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
                {folders.map((folder) => (
                  <div
                    className="flex items-center gap-3 rounded-xl border bg-background p-4 shadow-sm"
                    key={folder.folderId}
                  >
                    <FolderIcon className="size-8 fill-blue-500/20 text-blue-600" />
                    <div className="min-w-0">
                      <p className="truncate text-sm font-semibold">{folder.name}</p>
                      <p className="text-muted-foreground text-xs">Folder</p>
                    </div>
                  </div>
                ))}
              </div>
            ) : (
              <div className="flex flex-1 items-center justify-center rounded-2xl border border-dashed text-sm text-muted-foreground">
                No recent folders
              </div>
            )}
          </TabsContent>
          <TabsContent value="starred" className="mt-4 flex min-h-0 flex-1 flex-col">
            <div className="flex flex-1 items-center justify-center rounded-2xl border border-dashed text-sm text-muted-foreground">
              No starred files
            </div>
          </TabsContent>
        </Tabs>
      </main>
    </div>
  )
}

export default HomeView
