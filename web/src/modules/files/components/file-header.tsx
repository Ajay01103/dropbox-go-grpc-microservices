"use client"

import { Search, UserPlus } from "lucide-react"

import { Avatar, AvatarFallback } from "@/components/ui/avatar"
import { Button } from "@/components/ui/button"
import { useAuth } from "@/lib/auth-context"
import { useBreadcrumbs } from "@/modules/files/api/use-files"

function getInitials(name?: string) {
  if (!name) return "AS"

  return (
    name
      .trim()
      .split(/\s+/)
      .slice(0, 2)
      .map((part) => part[0]?.toUpperCase() ?? "")
      .join("") || "AS"
  )
}

export function FileHeader({ folderId }: { folderId?: string }) {
  const { currentUser } = useAuth()
  const initials = getInitials(currentUser?.name)

  return (
    <header className="bg-background border-b">
      <div className="flex min-h-20 flex-wrap items-center gap-3 px-4 py-3 sm:px-6 lg:px-10">
        <div className="relative min-w-48 flex-1 basis-full sm:basis-64 lg:max-w-3xl">
          <Search
            aria-hidden="true"
            className="text-muted-foreground pointer-events-none absolute top-1/2 left-4 size-4 -translate-y-1/2"
          />
          <input
            aria-label="Search files"
            className="border-input bg-background placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-ring/30 h-10 w-full rounded-xl border pr-4 pl-11 text-sm transition-shadow outline-none focus-visible:ring-2"
            placeholder="Search"
            type="search"
          />
        </div>

        <div className="ml-auto flex items-center gap-2 sm:gap-3">
          <Button
            className="hidden h-10 rounded-xl px-3 sm:inline-flex"
            variant="outline"
          >
            <UserPlus aria-hidden="true" />
            <span>Invite members</span>
          </Button>
          <Button className="h-10 rounded-xl bg-lime-400 px-3 text-slate-950 hover:bg-lime-300 sm:px-4">
            Click to upgrade
          </Button>
          <Button
            aria-label="Open account menu"
            className="size-10 rounded-full p-0"
            variant="ghost"
          >
            <Avatar className="size-9 bg-violet-200 text-violet-900">
              <AvatarFallback className="bg-violet-200 text-xs font-semibold text-violet-900">
                {initials}
              </AvatarFallback>
            </Avatar>
          </Button>
        </div>
      </div>

      <div className="px-4 pt-2 pb-4 sm:px-6 lg:px-10">
        <h1 className="text-2xl font-medium tracking-tight">{folderId ? <FolderTitle folderId={folderId} /> : "All Files"}</h1>
      </div>
    </header>
  )
}

// FolderTitle resolves the viewed folder's name from the breadcrumb chain
// (last crumb) so the header tracks navigation without another RPC shape.
function FolderTitle({ folderId }: { folderId: string }) {
  const { data: crumbs } = useBreadcrumbs(folderId)
  const current = crumbs?.[crumbs.length - 1]
  return <>{current?.name || "My Files"}</>
}
