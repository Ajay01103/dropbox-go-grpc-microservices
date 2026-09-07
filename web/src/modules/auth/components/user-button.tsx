"use client"

import { useMemo } from "react"
import { Avatar as createAvatar, Style } from "@dicebear/core"
import initials from "@dicebear/styles/initials.json"
import { LogOut, Mail, UserCircle2 } from "lucide-react"

import { useAuth } from "@/lib/auth-context"
import { Button } from "@/components/ui/button"
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { useLogout } from "@/modules/auth/api/use-logout"
import { useRouter } from "next/navigation"

const initialsStyle = new Style(initials)

function getInitials(name?: string) {
  if (!name) return "U"

  const parts = name.trim().split(/\s+/).slice(0, 2)

  return parts.map((p) => p[0]?.toUpperCase() ?? "").join("") || "U"
}

export function UserButton() {
  const { currentUser: user, isLoadingUser: isLoading } = useAuth()
  const logoutMutation = useLogout()
  const router = useRouter()

  const displayName = user?.name ?? "User"

  const initialsText = useMemo(() => getInitials(displayName), [displayName])

  const avatarUri = useMemo(() => {
    const svg = new createAvatar(initialsStyle, {
      seed: displayName,
      borderRadius: 50,
      fontWeight: 600,
      // fontSize: 40,
      // chars: 2,
      backgroundColor: ["b6e3f4", "c0aede", "d1d4f9", "ffd5dc", "ffdfbf"],
      textColor: ["1f2937"],
    }).toDataUri()

    return svg
  }, [displayName])

  const handleLogout = async () => {
    await logoutMutation.mutateAsync()
    router.refresh()
  }

  return (
    <Popover>
      <PopoverTrigger
        render={
          <Button
            variant="ghost"
            className="h-10 w-full justify-start gap-3 rounded-xl px-2"
          >
            <Avatar className="size-8">
              <AvatarImage
                src={avatarUri}
                alt={displayName}
              />
              <AvatarFallback>{isLoading ? "..." : initialsText}</AvatarFallback>
            </Avatar>

            <div className="min-w-0 flex-1 text-left">
              <p className="truncate text-sm font-medium">
                {isLoading ? "Loading..." : displayName}
              </p>

              <p className="text-muted-foreground truncate text-xs">{user?.email ?? "No email"}</p>
            </div>
          </Button>
        }
      />

      <PopoverContent
        side="right"
        align="end"
        className="w-72 rounded-xl p-2"
      >
        <div className="rounded-lg border p-3">
          <div className="flex items-center gap-3">
            <Avatar className="size-10">
              <AvatarImage
                src={avatarUri}
                alt={displayName}
              />
              <AvatarFallback>{initialsText}</AvatarFallback>
            </Avatar>

            <div className="min-w-0">
              <p className="truncate text-sm font-semibold">{displayName}</p>

              <p className="text-muted-foreground truncate text-xs">{user?.email ?? "No email"}</p>
            </div>
          </div>

          <div className="mt-3 space-y-1">
            <div className="text-muted-foreground flex items-center gap-2 text-xs">
              <UserCircle2 className="size-3.5" />
              <span className="truncate">ID: {user?.userId ?? "-"}</span>
            </div>

            <div className="text-muted-foreground flex items-center gap-2 text-xs">
              <Mail className="size-3.5" />
              <span className="truncate">{user?.email ?? "-"}</span>
            </div>
          </div>
        </div>

        <Button
          variant="ghost"
          className="text-destructive hover:text-destructive mt-2 w-full justify-start"
          onClick={handleLogout}
          disabled={logoutMutation.isPending}
        >
          <LogOut className="size-4" />
          {logoutMutation.isPending ? "Logging out..." : "Logout"}
        </Button>
      </PopoverContent>
    </Popover>
  )
}
