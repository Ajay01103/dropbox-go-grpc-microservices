"use client";

import { useEffect, useState } from "react";
import {
  ChevronDown,
  Download,
  ExternalLink,
  FileText,
  Film,
  Image as ImageIcon,
  Link2,
  MessageSquare,
  MoreHorizontal,
  Music2,
  Pencil,
  Sparkles,
  X,
  ZoomIn,
  ZoomOut,
} from "lucide-react";

import type { Item } from "@/gen/pb/metadata/metadata_pb";
import { Button } from "@/components/ui/button";
import {
  Drawer,
  DrawerContent,
  DrawerDescription,
  DrawerHeader,
  DrawerTitle,
} from "@/components/ui/drawer";
import { useThumbnailUrl } from "@/modules/files/api/use-files";
import { cn, formatBytes, formatRelativeTime } from "@/lib/utils";

/* ------------------------------------------------------------------ */
/*  Responsive breakpoint hook                                         */
/*  Below 768px -> the drawer becomes a bottom sheet (mobile pattern). */
/*  At/above 768px -> the drawer becomes a right-side panel.           */
/* ------------------------------------------------------------------ */
function useIsMobile(breakpointPx = 768) {
  const [isMobile, setIsMobile] = useState(false);

  useEffect(() => {
    const mql = window.matchMedia(`(max-width: ${breakpointPx - 1}px)`);
    const update = () => setIsMobile(mql.matches);
    update();
    mql.addEventListener("change", update);
    return () => mql.removeEventListener("change", update);
  }, [breakpointPx]);

  return isMobile;
}

/* ------------------------------------------------------------------ */
/*  Real-data helpers — everything derives from the pb Item            */
/* ------------------------------------------------------------------ */

function fileExtension(filename: string): string {
  const dot = filename.lastIndexOf(".");
  return dot > 0 && dot < filename.length - 1 ? filename.slice(dot + 1).toUpperCase() : "FILE";
}

type PreviewKind = "image" | "video" | "audio" | "doc" | "other";

function previewKind(contentType: string | undefined): PreviewKind {
  if (contentType?.startsWith("image/")) return "image";
  if (contentType?.startsWith("video/")) return "video";
  if (contentType?.startsWith("audio/")) return "audio";
  if (contentType === "application/pdf" || contentType?.startsWith("text/")) return "doc";
  return "other";
}

function KindIcon({ kind, className }: { kind: PreviewKind; className: string }) {
  switch (kind) {
    case "image":
      return <ImageIcon className={className} strokeWidth={1.75} />;
    case "video":
      return <Film className={className} strokeWidth={1.75} />;
    case "audio":
      return <Music2 className={className} strokeWidth={1.75} />;
    default:
      return <FileText className={className} strokeWidth={1.75} />;
  }
}

/* ------------------------------------------------------------------ */
/*  Preview surface — the actual content area inside the drawer        */
/*  Images load the presigned S3 URL from GetThumbnailURL; every       */
/*  other kind renders an honest "not previewable" state.              */
/* ------------------------------------------------------------------ */
function PreviewSurface({
  item,
  kind,
  url,
  urlError,
  urlLoading,
  zoom,
}: {
  item: Item;
  kind: PreviewKind;
  url: string | undefined;
  urlError: unknown;
  urlLoading: boolean;
  zoom: number;
}) {
  const name = item.details.case === "file" ? item.details.value.filename : item.itemId;

  if (kind === "image") {
    if (urlLoading) {
      return (
        <div className="flex h-full w-full items-center justify-center">
          <div className="bg-muted size-10 animate-pulse rounded-full" />
        </div>
      );
    }
    if (urlError || !url) {
      return (
        <div className="text-muted-foreground flex h-full w-full flex-col items-center justify-center gap-3 p-8">
          <ImageIcon className="size-14" strokeWidth={1.5} />
          <p className="text-sm">Thumbnail is not available yet.</p>
        </div>
      );
    }
    return (
      <div className="flex h-full w-full items-center justify-center p-4 sm:p-10">
        {/* eslint-disable-next-line @next/next/no-img-element -- presigned S3 URLs expire, next/image optimization adds no value */}
        <img
          alt={name}
          className="max-h-full max-w-full rounded-md shadow-2xl transition-transform duration-150"
          src={url}
          style={{ transform: `scale(${zoom / 100})` }}
        />
      </div>
    );
  }

  return (
    <div className="text-muted-foreground flex h-full w-full flex-col items-center justify-center gap-3 p-8">
      <KindIcon className="size-16" kind={kind} />
      <p className="text-sm">Preview is not available for this file type yet.</p>
    </div>
  );
}

/* ------------------------------------------------------------------ */
/*  Action bar: horizontally scrollable so it survives narrow screens  */
/*  Open/Download work for images (presigned URL); the rest stay       */
/*  visibly inert until their features ship.                           */
/* ------------------------------------------------------------------ */
function ActionBar({
  canOpenUrl,
  url,
  onUnavailable,
}: {
  canOpenUrl: boolean;
  url: string | undefined;
  onUnavailable: (action: string) => void;
}) {
  const openInTab = () => {
    if (canOpenUrl && url) window.open(url, "_blank", "noopener,noreferrer");
    else onUnavailable("Open in");
  };

  const actions = [
    { label: "Open in", icon: ExternalLink, onClick: openInTab },
    { label: "Edit", icon: Pencil, onClick: () => onUnavailable("Edit") },
    { label: "Enhance", icon: Sparkles, onClick: () => onUnavailable("Enhance") },
    { label: "Download", icon: Download, onClick: openInTab },
  ];
  return (
    <div className="flex items-center gap-1 overflow-x-auto border-b px-3 py-2 sm:px-5">
      {actions.map(({ label, icon: Icon, onClick }) => (
        <Button
          className="shrink-0 gap-1.5"
          key={label}
          onClick={onClick}
          size="sm"
          variant="ghost"
        >
          <Icon className="h-4 w-4" strokeWidth={1.75} />
          {label}
          {label === "Open in" || label === "Edit" || label === "Enhance" ? (
            <ChevronDown className="text-muted-foreground h-3.5 w-3.5" />
          ) : null}
        </Button>
      ))}
      <Button className="text-muted-foreground ml-auto shrink-0" size="icon" variant="ghost">
        <MoreHorizontal className="h-4 w-4" />
      </Button>
    </div>
  );
}

/* ------------------------------------------------------------------ */
/*  Zoom control pill, pinned bottom-right of the preview area         */
/* ------------------------------------------------------------------ */
function ZoomControl({
  zoom,
  setZoom,
}: {
  zoom: number;
  setZoom: (updater: (z: number) => number) => void;
}) {
  return (
    <div className="bg-background absolute bottom-4 right-4 flex items-center gap-1 rounded-full border px-1.5 py-1 shadow-md">
      <Button
        aria-label="Zoom out"
        className="h-7 w-7 rounded-full"
        disabled={zoom <= 25}
        onClick={() => setZoom((z) => Math.max(25, z - 25))}
        size="icon"
        variant="ghost"
      >
        <ZoomOut className="h-3.5 w-3.5" />
      </Button>
      <span className="w-9 text-center text-xs font-medium">{zoom}%</span>
      <Button
        aria-label="Zoom in"
        className="h-7 w-7 rounded-full"
        disabled={zoom >= 200}
        onClick={() => setZoom((z) => Math.min(200, z + 25))}
        size="icon"
        variant="ghost"
      >
        <ZoomIn className="h-3.5 w-3.5" />
      </Button>
    </div>
  );
}

/* ------------------------------------------------------------------ */
/*  The responsive preview drawer itself                               */
/* ------------------------------------------------------------------ */
export function FilePreviewDrawer({
  item,
  open,
  onOpenChange,
}: {
  item: Item | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const isMobile = useIsMobile();
  const [zoom, setZoom] = useState(100);

  const file = item?.details.case === "file" ? item.details.value : undefined;
  const kind = previewKind(file?.contentType);
  const name = file?.filename ?? item?.itemId ?? "File preview";
  const ext = file ? fileExtension(file.filename) : "";
  const size = file ? formatBytes(file.sizeBytes) : "";
  const when = formatRelativeTime(file?.createdAt);

  const thumbnail = useThumbnailUrl(
    item?.itemId ?? "",
    file?.thumbnailStatus ?? "",
    file?.thumbnailKey ?? "",
  );
  const showUrl = open && kind === "image";

  useEffect(() => {
    if (open) setZoom(100);
  }, [open, item?.itemId]);  // Right side-panel on desktop/tablet, bottom sheet on mobile. The base
  // Drawer positions itself from swipeDirection — never pass manual inset
  // classes on DrawerContent or the panel lands in the wrong corner.
  const swipeDirection = isMobile ? "down" : "right"

  return (
    <Drawer
      onOpenChange={onOpenChange}
      open={open}
      swipeDirection={swipeDirection}
    >
      <DrawerContent
        className={cn(
          isMobile
            ? "h-[92vh]"
            : "h-full w-full rounded-none border-l sm:max-w-2xl",
        )}
      >
        {/* Visually-hidden semantics for screen readers / a11y tree */}
        <DrawerHeader className="sr-only">
          <DrawerTitle>{name}</DrawerTitle>
          <DrawerDescription>{file ? `${ext} · ${size}` : "File preview panel"}</DrawerDescription>
        </DrawerHeader>

        {item && file && (
          <div className="bg-background flex h-full flex-col">
            {/* Header */}
            <div className="flex items-start gap-3 px-3 py-3 sm:px-5 sm:py-4">
              <Button
                aria-label="Close preview"
                className="text-muted-foreground mt-0.5 shrink-0"
                onClick={() => onOpenChange(false)}
                size="icon"
                variant="ghost"
              >
                <X className="h-5 w-5" />
              </Button>

              <div className="min-w-0 flex-1">
                <p className="truncate text-sm font-semibold sm:text-base" title={name}>
                  {name}
                </p>
                <p className="text-muted-foreground truncate text-xs sm:text-sm">
                  {[ext, size, when].filter(Boolean).join(" · ")}
                </p>
              </div>

              <div className="hidden shrink-0 items-center gap-1 sm:flex">
                <Button
                  aria-label="Comments"
                  className="text-muted-foreground"
                  onClick={() => window.alert("Comments are not available yet.")}
                  size="icon"
                  variant="ghost"
                >
                  <MessageSquare className="h-4 w-4" />
                </Button>
                <Button
                  aria-label="Copy link"
                  className="text-muted-foreground"
                  onClick={() => void navigator.clipboard?.writeText(item.itemId)}
                  size="icon"
                  variant="ghost"
                >
                  <Link2 className="h-4 w-4" />
                </Button>
                <Button
                  onClick={() => window.alert("Share is not available yet.")}
                  size="sm"
                  variant="outline"
                >
                  Share
                </Button>
              </div>
            </div>

            <ActionBar
              canOpenUrl={showUrl && Boolean(thumbnail.data)}
              onUnavailable={(action) => window.alert(`${action} is not available yet.`)}
              url={showUrl ? thumbnail.data : undefined}
            />

            {/* Preview area */}
            <div className="bg-muted/30 relative flex-1 overflow-auto">
              <PreviewSurface
                item={item}
                kind={kind}
                url={showUrl ? thumbnail.data : undefined}
                urlError={showUrl ? thumbnail.error : undefined}
                urlLoading={showUrl ? thumbnail.isLoading : false}
                zoom={zoom}
              />
              {kind === "image" && showUrl && !thumbnail.isLoading && thumbnail.data && (
                <ZoomControl setZoom={setZoom} zoom={zoom} />
              )}
            </div>
          </div>
        )}
      </DrawerContent>
    </Drawer>
  );
}
