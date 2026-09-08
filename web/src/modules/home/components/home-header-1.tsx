"use client";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { Search, UserPlus, Plus } from "lucide-react";
import { SidebarTrigger } from "@/components/ui/sidebar";

export const HomeHeader1 = () => {
  return (
    <header className="flex min-h-20 items-center gap-3 border-b px-4 py-4 sm:gap-4 sm:px-8">
      <SidebarTrigger className="shrink-0 md:hidden" />
      <Button className="h-9 rounded-xl px-4" aria-label="Create new item">
        <Plus />
        New
      </Button>
      <div className="relative min-w-0 flex-1">
        <Search className="absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          aria-label="Search files"
          placeholder="Search"
          className="h-10 rounded-xl pl-9 shadow-none"
        />
      </div>
      <Button variant="secondary" className="hidden h-10 rounded-xl px-4 lg:inline-flex">
        <UserPlus />
        Invite members
      </Button>
      <Button className="hidden h-10 rounded-xl bg-lime-400 px-4 text-foreground hover:bg-lime-500 sm:inline-flex">
        Click to upgrade
      </Button>
      <Avatar className="size-9 shrink-0 bg-[#cbb5f4] text-foreground">
        <AvatarFallback className="bg-transparent text-xs font-semibold">AS</AvatarFallback>
      </Avatar>
    </header>
  );
};
