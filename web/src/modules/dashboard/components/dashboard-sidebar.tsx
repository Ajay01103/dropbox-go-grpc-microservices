"use client"

import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarRail,
  SidebarTrigger,
} from "@/components/ui/sidebar"
import { UserButton } from "@/modules/auth/components/user-button"
import { Headphones, LayoutGrid, Settings, type LucideIcon } from "lucide-react"
import Image from "next/image"
import Link from "next/link"
import { usePathname } from "next/navigation"

interface MenuItem {
  title: string
  url?: string
  icon: LucideIcon
  onClick?: () => void
  match?: "exact" | "prefix"
}

interface NavSectionProps {
  label?: string
  items: MenuItem[]
  pathname: string
}

const NavSection = ({ label, items, pathname }: NavSectionProps) => {
  const isItemActive = (item: MenuItem) => {
    if (!item.url) {
      return false
    }

    if (item.match === "exact") {
      return pathname === item.url
    }

    return item.url === "/" ? pathname === "/" : pathname.startsWith(item.url)
  }

  return (
    <SidebarGroup>
      {label && (
        <SidebarGroupLabel className="text-muted-foreground text-[13px] uppercase">
          {label}
        </SidebarGroupLabel>
      )}
      <SidebarGroupContent>
        <SidebarMenu>
          {items.map((item) => (
            <SidebarMenuItem key={item.title}>
              {item.url ? (
                <Link href={item.url}>
                  <SidebarMenuButton
                    isActive={isItemActive(item)}
                    tooltip={item.title}
                    className="data-[active=true]:border-border h-9 border border-transparent px-3 py-2 text-[13px] font-medium tracking-tight data-[active=true]:shadow-[0px_1px_1px_0px_rgba(44,54,53,0.03),inset_0px_0px_0px_2px_white]"
                  >
                    <item.icon />
                    <span>{item.title}</span>
                  </SidebarMenuButton>
                </Link>
              ) : (
                <SidebarMenuButton
                  isActive={false}
                  onClick={item.onClick}
                  tooltip={item.title}
                  className="data-[active=true]:border-border h-9 border border-transparent px-3 py-2 text-[13px] font-medium tracking-tight data-[active=true]:shadow-[0px_1px_1px_0px_rgba(44,54,53,0.03),inset_0px_0px_0px_2px_white]"
                >
                  <item.icon />
                  <span>{item.title}</span>
                </SidebarMenuButton>
              )}
            </SidebarMenuItem>
          ))}
        </SidebarMenu>
      </SidebarGroupContent>
    </SidebarGroup>
  )
}

export const DashboardSidebar = () => {
  const pathname = usePathname()

  const dashboardeNavigationItems = [
    {
      title: "Overview",
      url: `/workspace/}`,
      icon: LayoutGrid,
      match: "exact",
    },
  ]

  const othersMenuItems: MenuItem[] = [
    {
      title: "Settings",
      icon: Settings,
    },
    {
      title: "Help and support",
      url: "mailto:business@codewithantonio.com",
      icon: Headphones,
    },
  ]

  return (
    <>
      <Sidebar collapsible="icon">
        <SidebarHeader className="flex flex-col gap-3 pt-4">
          <div className="flex items-center gap-2 pl-1 group-data-[collapsible=icon]:justify-center group-data-[collapsible=icon]:pl-0">
            <Image
              src="/logo.svg"
              alt="Resonance"
              width={24}
              height={24}
              className="rounded-sm"
            />
            <span className="text-foreground text-lg font-semibold tracking-tighter group-data-[collapsible=icon]:hidden">
              Resonance
            </span>
            <SidebarTrigger className="ml-auto group-data-[collapsible=icon]:hidden" />
          </div>
          <div className="space-y-2 px-1 group-data-[collapsible=icon]:hidden">
            <SidebarGroupLabel className="text-muted-foreground px-1 text-[13px] uppercase">
              Workspace
            </SidebarGroupLabel>
          </div>
          <div className="flex items-center justify-start group-data-[collapsible=icon]:hidden"></div>
        </SidebarHeader>
        <div className="border-border border-b border-dashed" />
        <SidebarContent>
          {dashboardeNavigationItems.length > 0 ? (
            <NavSection
              label="Workspace"
              items={dashboardeNavigationItems}
              pathname={pathname}
            />
          ) : null}
          <NavSection
            items={[]}
            pathname={pathname}
          />
          <NavSection
            label="Others"
            items={othersMenuItems}
            pathname={pathname}
          />
        </SidebarContent>
        <div className="border-border border-b border-dashed" />
        <SidebarFooter className="gap-3 py-3">
          {/*<UsageContainer />*/}
          <SidebarMenu>
            <SidebarMenuItem>
              <UserButton />
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarFooter>
        <SidebarRail />
      </Sidebar>
    </>
  )
}
