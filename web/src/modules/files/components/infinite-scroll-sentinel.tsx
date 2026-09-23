"use client"

import { useEffect, useRef } from "react"

interface InfiniteScrollSentinelProps {
  hasNextPage: boolean
  isFetchingNextPage: boolean
  onLoadMore: () => void
  root?: Element | null
}

export function InfiniteScrollSentinel({
  hasNextPage,
  isFetchingNextPage,
  onLoadMore,
  root = null,
}: InfiniteScrollSentinelProps) {
  const sentinelRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const sentinel = sentinelRef.current
    if (!sentinel || !hasNextPage) return

    const observer = new IntersectionObserver(
      ([entry]) => {
        if (entry?.isIntersecting && !isFetchingNextPage) {
          onLoadMore()
        }
      },
      { root, rootMargin: "400px" },
    )

    observer.observe(sentinel)
    return () => observer.disconnect()
  }, [hasNextPage, isFetchingNextPage, onLoadMore, root])

  return (
    <div
      ref={sentinelRef}
      aria-hidden="true"
      className="h-px w-full"
    />
  )
}
