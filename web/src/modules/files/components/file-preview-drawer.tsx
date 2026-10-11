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
import { useContentUrl } from "@/modules/files/api/use-content-url";
import { useDownloadFile } from "@/modules/files/api/use-download";
import { useThumbnailUrl } from "@/modules/files/api/use-files";
import { DocxPreviewPanel } from "@/modules/files/components/docx-preview-panel";
import { PdfPreviewPanel } from "@/modules/files/components/pdf-preview-panel";
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

type PreviewKind = "image" | "video" | "audio" | "pdf" | "docx" | "doc" | "other";

const DOCX_CONTENT_TYPE =
  "application/vnd.openxmlformats-officedocument.wordprocessingml.document";

function previewKind(
  contentType: string | undefined,
  filename: string | undefined,
): PreviewKind {
  // A DOCX is a zip container whose stored content type is client-supplied (the
  // upload service trusts what the browser sent), so the extension is the more
  // reliable of the two signals. Legacy .doc stays "other": the viewer marks it
  // limited and experimental, and a download is the honest fallback.
  if (filename?.toLowerCase().endsWith(".docx")) return "docx";
  if (contentType === DOCX_CONTENT_TYPE) return "docx";
  if (contentType?.startsWith("image/")) return "image";
  if (contentType?.startsWith("video/")) return "video";
  if (contentType?.startsWith("audio/")) return "audio";
  if (contentType === "application/pdf") return "pdf";
  if (contentType?.startsWith("text/")) return "doc";
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
/*  The preview pipeline generates a JPEG for image and video families */
/*  (server-side), so those two render the presigned URL. PDFs and     */
/*  DOCX files are rendered by the real document viewers instead (no   */
/*  server rendition exists for either), and audio/doc/other keep the  */
/*  honest "not previewable" state.                                    */
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

  // image | video get a server-generated preview JPEG; the remaining kinds
  // have no preview yet.
  if (kind === "image" || kind === "video") {
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
          <KindIcon className="size-14" kind={kind} />
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
/*  Open uses the preview URL; Download mints a download ticket and    */
/*  hands the content URL to the browser's native download manager.    */
/* ------------------------------------------------------------------ */
function ActionBar({
  canOpenUrl,
  url,
  onUnavailable,
  onDownload,
  downloadPending = false,
}: {
  canOpenUrl: boolean;
  url: string | undefined;
  onUnavailable: (action: string) => void;
  onDownload: () => void;
  downloadPending?: boolean;
}) {
  const openInTab = () => {
    if (canOpenUrl && url) window.open(url, "_blank", "noopener,noreferrer");
    else onUnavailable("Open in");
  };

  const actions = [
    { label: "Open in", icon: ExternalLink, onClick: openInTab },
    { label: "Edit", icon: Pencil, onClick: () => onUnavailable("Edit") },
    { label: "Enhance", icon: Sparkles, onClick: () => onUnavailable("Enhance") },
    { label: "Download", icon: Download, onClick: onDownload, disabled: downloadPending },
  ];
  return (
    <div className="flex items-center gap-1 overflow-x-auto border-b px-3 py-2 sm:px-5">
      {actions.map(({ label, icon: Icon, onClick, disabled }) => (
        <Button
          className="shrink-0 gap-1.5"
          disabled={disabled}
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
  const { downloadFile, isPending: isDownloadPending } = useDownloadFile();

  const file = item?.details.case === "file" ? item.details.value : undefined;
  const kind = previewKind(file?.contentType, file?.filename);
  const name = file?.filename ?? item?.itemId ?? "File preview";
  const ext = file ? fileExtension(file.filename) : "";
  const size = file ? formatBytes(file.sizeBytes) : "";
  const when = formatRelativeTime(file?.createdAt);

  const thumbnail = useThumbnailUrl(
    item?.itemId ?? "",
    file?.thumbnailStatus ?? "",
    file?.thumbnailKey ?? "",
  );
  // Documents render the real file rather than a server-generated preview JPEG:
  // the viewer streams bytes with range requests from a short-lived ticket URL
  // (see useContentUrl). Image/video keep the JPEG preview. The ticket is only
  // minted while a document preview is actually open.
  const isPdf = kind === "pdf";
  const isDocx = kind === "docx";
  const isDocument = isPdf || isDocx;
  const content = useContentUrl(item?.itemId ?? "", { enabled: open && isDocument });
  const showUrl = open && (kind === "image" || kind === "video");

  useEffect(() => {
    if (open) setZoom(100);
  }, [open, item?.itemId]);

  // Right side-panel on desktop/tablet, bottom sheet on mobile. The base
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
            : // Documents get a wider panel: a fitted page, the viewer toolbar
              // and the DOCX thumbnail sidebar need more room than the
              // image-width panel, and squeezing them only produces a
              // horizontally scrolling page.
              [
                "h-full w-full rounded-none border-l",
                isDocument ? "sm:max-w-4xl" : "sm:max-w-2xl",
              ],
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
              canOpenUrl={showUrl ? Boolean(thumbnail.data) : Boolean(content.data?.url)}
              downloadPending={isDownloadPending(item.itemId)}
              onDownload={() => void downloadFile(item.itemId, name)}
              onUnavailable={(action) => window.alert(`${action} is not available yet.`)}
              url={showUrl ? thumbnail.data : content.data?.url}
            />

            {/* Preview area — the document viewers own their own scroll/zoom,
                every other kind is panned/zoomed by the drawer's zoom pill. */}
            <div
              className={cn(
                "bg-muted/30 relative flex-1",
                isDocument ? "overflow-hidden" : "overflow-auto",
              )}
            >
              {isPdf ? (
                <PdfPreviewPanel
                  documentKey={item.itemId}
                  filename={name}
                  isError={content.isError}
                  isLoading={content.isFetching}
                  key={item.itemId}
                  onRetry={() => void content.refetch()}
                  url={content.data?.url}
                />
              ) : isDocx ? (
                <DocxPreviewPanel
                  documentKey={item.itemId}
                  filename={name}
                  isError={content.isError}
                  isLoading={content.isFetching}
                  key={item.itemId}
                  onRetry={() => void content.refetch()}
                  url={content.data?.url}
                />
              ) : (
                <PreviewSurface
                  item={item}
                  kind={kind}
                  url={showUrl ? thumbnail.data : undefined}
                  urlError={showUrl ? thumbnail.error : undefined}
                  urlLoading={showUrl ? thumbnail.isLoading : false}
                  zoom={zoom}
                />
              )}
              {showUrl && !thumbnail.isLoading && thumbnail.data && (
                <ZoomControl setZoom={setZoom} zoom={zoom} />
              )}
            </div>
          </div>
        )}
      </DrawerContent>
    </Drawer>
  );
}
