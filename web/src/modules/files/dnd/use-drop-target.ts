"use client"

import { useEffect, useRef, useState } from "react"
import { dropTargetForElements } from "@atlaskit/pragmatic-drag-and-drop/element/adapter"

import { isItemDragData, type FileViewDropData } from "./dnd-types"

// useDropTargetFromCurrent registers an element as a drop target that
// accepts file-view items, reading the target payload through a ref so the
// hook stays referentially stable while item data changes. Returns whether
// a compatible drag is currently hovering, so callers can render the
// "drop into folder" affordance.
//
// The drop target is registered for the LIFETIME of the element, not only
// while a drag is in progress: pragmatic drag and drop only consults
// drop targets registered at drag start, so late registration during
// onDragStart races with the first dragover and makes the card untargetable.
export function useDropTargetFromCurrent(getData: () => FileViewDropData) {
  const ref = useRef<HTMLElement | null>(null)
  const [isDragOver, setIsDragOver] = useState(false)
  const dataRef = useRef(getData)
  dataRef.current = getData

  useEffect(() => {
    const el = ref.current
    if (!el) return

    return dropTargetForElements({
      element: el,
      getData: () => dataRef.current(),
      canDrop: ({ source }) => isItemDragData(source.data),
      onDragEnter: () => setIsDragOver(true),
      onDragLeave: () => setIsDragOver(false),
      onDrop: () => setIsDragOver(false),
    })
  }, [])

  return { ref, isDragOver }
}
