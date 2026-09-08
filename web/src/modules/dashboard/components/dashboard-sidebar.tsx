"use client";

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
} from "@/components/ui/sidebar";
import { UserButton } from "@/modules/auth/components/user-button";
import {
  Bell,
  ChevronDown,
  CircleHelp,
  // FolderOpen,
  Grid3X3,
  Home,
  Headphones,
  MonitorDown,
  PanelLeft,
  Plus,
  Settings,
  Group,
  Trash2,
  type LucideIcon,
  File,
} from "lucide-react";
import Image from "next/image";
import Link from "next/link";
import { usePathname } from "next/navigation";

interface MenuItem {
  title: string;
  url?: string;
  icon: LucideIcon;
  onClick?: () => void;
  match?: "exact" | "prefix";
}

interface NavSectionProps {
  label?: string;
  items: MenuItem[];
  pathname: string;
}

const NavSection = ({ label, items, pathname }: NavSectionProps) => {
  const isItemActive = (item: MenuItem) => {
    if (!item.url) {
      return false;
    }

    if (item.match === "exact") {
      return pathname === item.url;
    }

    return item.url === "/" ? pathname === "/" : pathname.startsWith(item.url);
  };

  return (
    <SidebarGroup className="px-2 py-1">
      {label && (
        <SidebarGroupLabel className="text-muted-foreground h-8 px-3 text-[11px] font-semibold uppercase tracking-[0.08em]">
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
                    className="h-9 rounded-lg border border-transparent px-3 py-2 text-[13px] font-medium tracking-tight data-[active=true]:border-border data-[active=true]:bg-muted data-[active=true]:shadow-[0px_1px_1px_0px_rgba(44,54,53,0.03),inset_0px_0px_0px_2px_white]"
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
                  className="h-9 rounded-lg border border-transparent px-3 py-2 text-[13px] font-medium tracking-tight"
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
  );
};

export const DashboardSidebar = () => {
  const pathname = usePathname();

  const dashboardNavigationItems: MenuItem[] = [
    {
      title: "Home",
      url: "/home",
      icon: Home,
      match: "exact",
    },
    {
      title: "All files",
      url: "/files",
      icon: File,
      match: "exact",
    },
    // {
    //   title: "Photos",
    //   icon: FolderOpen,
    // },
    {
      title: "Shared",
      url: "/shared-files",
      icon: Group,
      match: "exact",
    },
    {
      title: "File requests",
      icon: PanelLeft,
    },
    {
      title: "Deleted files",
      url: "/deleted-files",
      icon: Trash2,
      match: "exact",
    },
  ];

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
  ];

  return (
    <Sidebar
      collapsible="offcanvas"
      className="w-87.5 shrink-0 border-r-0 bg-background [--sidebar-width:350px]"
    >
      <div className="flex size-full flex-row">
        <aside className="flex w-19 shrink-0 flex-col items-center border-r bg-muted/35 py-5">
          <div className="flex h-9 w-9 items-center justify-center">
            <Image src="/logo.svg" alt="Resonance" width={24} height={24} className="rounded-sm" />
          </div>
          <nav
            className="mt-8 flex flex-1 flex-col items-center gap-4"
            aria-label="Primary navigation"
          >
            <SidebarMenuButton
              isActive={pathname === "/home"}
              tooltip="Home"
              className="size-10 justify-center rounded-xl"
            >
              <Home />
            </SidebarMenuButton>
            {/* <SidebarMenuButton
              isActive={pathname.startsWith("/files")}
              tooltip="Folders"
              className="size-10 justify-center rounded-xl"
            >
              <FolderOpen />
            </SidebarMenuButton> */}
            <SidebarMenuButton tooltip="Activity" className="size-10 justify-center rounded-xl">
              <Bell />
            </SidebarMenuButton>
            <SidebarMenuButton tooltip="More" className="size-10 justify-center rounded-xl">
              <Grid3X3 />
            </SidebarMenuButton>
          </nav>
          <div className="flex flex-col items-center gap-4">
            <SidebarMenuButton tooltip="Install app" className="size-10 justify-center rounded-xl">
              <MonitorDown />
            </SidebarMenuButton>
            <SidebarMenuButton tooltip="Help" className="size-10 justify-center rounded-xl">
              <CircleHelp />
            </SidebarMenuButton>
          </div>
        </aside>

        <div className="flex min-w-0 flex-1 flex-col bg-background">
          <SidebarHeader className="gap-3 border-b px-5 py-5">
            <div className="flex items-center gap-2">
              <span className="text-foreground text-lg font-semibold tracking-tighter">
                Resonance
              </span>
            </div>
            <button className="flex items-center gap-2 text-left text-sm font-medium text-foreground">
              <span className="size-2 rounded-full bg-lime-500" />
              Personal workspace
              <ChevronDown className="ml-auto size-4 text-muted-foreground" />
            </button>
          </SidebarHeader>
          <SidebarContent>
            <div className="flex items-center justify-between px-5 pb-1 pt-5">
              <span className="text-xs font-semibold uppercase tracking-[0.08em] text-muted-foreground">
                Quick access
              </span>
              <button
                className="rounded-md p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
                aria-label="Add quick access item"
              >
                <Plus className="size-4" />
              </button>
            </div>
            <NavSection items={dashboardNavigationItems} pathname={pathname} />
            <NavSection label="Others" items={othersMenuItems} pathname={pathname} />
          </SidebarContent>
          <SidebarFooter className="border-t px-3 py-3">
            <SidebarMenu>
              <SidebarMenuItem>
                <UserButton />
              </SidebarMenuItem>
            </SidebarMenu>
          </SidebarFooter>
        </div>
      </div>
    </Sidebar>
  );
};
