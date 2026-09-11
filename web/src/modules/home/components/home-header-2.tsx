"use client";

import { useState } from "react";
import { Button } from "@/components/ui/button";
import { FolderPlus, Settings2, Upload } from "lucide-react";
import { OPEN_UPLOAD_EVENT } from "@/modules/files/components/upload-drawer";
import { CreateFolderDialog } from "@/modules/files/components/create-folder-dialog";
import type { Folder } from "@/gen/pb/metadata/metadata_pb";

export const HomeHeader2 = ({ onFolderCreated }: { onFolderCreated: (folder: Folder) => void }) => {
  const [createFolderOpen, setCreateFolderOpen] = useState(false)

  return (
    <div className="flex min-h-20 flex-wrap items-center justify-between gap-4 px-4 py-4 sm:px-8">
      <div className="flex items-center gap-3">
        <h1 className="text-2xl font-semibold tracking-tight">Home</h1>
        <Button variant="ghost" size="icon" aria-label="Folder settings" className="rounded-lg">
          <Settings2 />
        </Button>
      </div>
      <div className="flex items-center gap-2 sm:ml-auto">
        <Button
          variant="secondary"
          className="h-9 rounded-xl px-3"
          onClick={() => window.dispatchEvent(new Event(OPEN_UPLOAD_EVENT))}
        >
          <Upload />
          Upload
        </Button>
        <Button
          variant="secondary"
          className="h-9 rounded-xl px-3"
          onClick={() => setCreateFolderOpen(true)}
        >
          <FolderPlus />
          <span className="hidden sm:inline">New folder</span>
        </Button>
        <span className="ml-2 hidden text-sm text-muted-foreground sm:inline">Only you</span>
      </div>
      <CreateFolderDialog
        open={createFolderOpen}
        onCreated={onFolderCreated}
        onOpenChange={setCreateFolderOpen}
      />
    </div>
  );
};
