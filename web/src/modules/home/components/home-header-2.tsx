"use client";

import { Button } from "@/components/ui/button";
import { ChevronDown, FolderPlus, Settings2, Upload } from "lucide-react";

export const HomeHeader2 = () => {
  return (
    <div className="flex min-h-20 flex-wrap items-center justify-between gap-4 px-4 py-4 sm:px-8">
      <div className="flex items-center gap-3">
        <h1 className="text-2xl font-semibold tracking-tight">All files</h1>
        <Button variant="ghost" size="icon" aria-label="Folder settings" className="rounded-lg">
          <Settings2 />
        </Button>
      </div>
      <div className="flex items-center gap-2 sm:ml-auto">
        <Button variant="secondary" className="h-9 rounded-xl px-3">
          <Upload />
          Upload
          <ChevronDown className="size-3.5" />
        </Button>
        <Button variant="secondary" className="h-9 rounded-xl px-3">
          <FolderPlus />
          <span className="hidden sm:inline">New folder</span>
        </Button>
        <span className="ml-2 hidden text-sm text-muted-foreground sm:inline">Only you</span>
      </div>
    </div>
  );
};
