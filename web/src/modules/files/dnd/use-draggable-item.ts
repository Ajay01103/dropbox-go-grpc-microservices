"use client"

import { useEffect, useRef } from "react"
import {
  draggable,
} from "@atlaskit/pragmatic-drag-and-drop/element/adapter"
import { setCustomNativeDragPreview } from "@atlaskit/pragmatic-drag-and-drop/element/set-custom-native-drag-preview"

import { isItemDragData, type ItemDragData } from "./dnd-types"

// useDraggableItem registers an element as draggable, carrying the item's
// drag payload. The registration reads the payload through a ref so the
// effect runs once per element while always publishing fresh data (a stale
// closure would move the wrong item after a rename or page refetch).
//
// Instead of the browser's default drag ghost (an opaque screenshot of the
// whole card, which looks messy over a grid), a small custom drag preview
// is rendered into a portal element: a pill with the item's icon and name.
export function useDraggableItem(data: ItemDragData) {
  const ref = useRef<HTMLElement | null>(null)
  const dataRef = useRef(data)
  dataRef.current = data

  useEffect(() => {
    const el = ref.current
    if (!el) return

    return draggable({
      element: el,
      getInitialData: () => dataRef.current,
      onDragStart: () => {
        el.dataset.dragging = "true"
        el.style.opacity = "1"
        el.style.filter = "none"
      },
      onDrop: () => {
        delete el.dataset.dragging
        el.style.removeProperty("opacity")
        el.style.removeProperty("filter")
      },
      onGenerateDragPreview({ nativeSetDragImage, source }) {
        const payload = isItemDragData(source.data) ? source.data : undefined
        if (!payload || !nativeSetDragImage) return

        setCustomNativeDragPreview({
          nativeSetDragImage,
          render: ({ container }: { container: HTMLElement }) => {
            const pill = document.createElement("div")
            pill.style.cssText = [
              "display:flex",
              "align-items:center",
              "gap:8px",
              "padding:8px 14px",
              "border-radius:9999px",
              "background:#172554",
              "border:1px solid #3b82f6",
              "box-shadow:0 8px 24px rgba(0,0,0,0.18)",
              "font:600 13px/1.2 system-ui, sans-serif",
              "color:#ffffff",
              "white-space:nowrap",
            ].join(";")

            const dot = document.createElement("span")
            dot.style.cssText =
              "flex:none;width:10px;height:10px;border-radius:9999px;background:" +
              (payload.itemType === "folder" ? "#60a5fa" : "#a3e635") + ";"
            const label = document.createElement("span")
            label.textContent = payload.name
            label.style.cssText =
              "max-width:240px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;"

            pill.append(dot, label)

            if (payload.dragCount > 1) {
              const count = document.createElement("span")
              count.textContent = `${payload.dragCount} files`
              count.style.cssText = [
                "display:inline-flex",
                "align-items:center",
                "padding:0 6px",
                "border-radius:9999px",
                "background:#2563eb",
                "color:white",
                "font:700 11px/20px system-ui, sans-serif",
              ].join(";")
              pill.append(count)
            }
            container.append(pill)
          },
        })
      },
    })
  }, [])

  return { ref }
}
