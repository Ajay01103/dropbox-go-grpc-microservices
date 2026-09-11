# Recent Files & All Files — Infinite Pagination Plan

Goal: make "All files" a single, correctly-paginated stream of folders + files, and add a Dropbox-style "Recents" view — without overloading Scylla or the frontend, using scroll-driven pagination throughout.

---

## Why this is needed

- `ListFolderContentsOwned` currently pages folders and files as two **independent** cursors and returns whichever token is non-empty. This silently drops pages once one list exhausts before the other.
- There is no per-owner recency index at all — only per-folder (`files_by_folder_updated`).

Fix: one merged, single-cursor table per sort mode for folder contents, and one owner-scoped time-series table for recents.

---

## Build order

Ship in this sequence. Each step is deployable on its own — nothing requires a big-bang cutover.

1. Migrations (new tables, additive only)
2. Repo layer (`ItemsRepo`, `RecentItemsRepo`)
3. Dual-write wiring into existing mutation paths
4. Backfill job for pre-existing data
5. Proto + RPC additions
6. Service + read path (single-cursor `ListFolderItemsOwned`, `ListRecentItemsOwned`)
7. gRPC interceptors (rate limit + timeout) — **before** flipping reads live
8. Cutover to the new read path
9. Frontend: infinite scroll hooks + sentinel
10. Contract: drop superseded old tables once stable

---

## 1. Migrations

### `folder_items_by_*` — unifies files + folders into one pageable stream per sort mode

```cql
CREATE TABLE IF NOT EXISTS folder_items_by_name (
  folder_id     uuid,
  name          text,
  item_type     text,      -- 'file' | 'folder'
  item_id       uuid,
  owner_id      uuid,
  size_bytes    bigint,    -- 0 for folders
  content_type  text,      -- '' for folders
  thumbnail_key text,
  created_at    timestamp,
  updated_at    timestamp,
  is_deleted    boolean,
  current       boolean,
  PRIMARY KEY ((folder_id), name, item_type, item_id)
) WITH CLUSTERING ORDER BY (name ASC, item_type ASC, item_id ASC)
  AND gc_grace_seconds = 86400;

-- same column shape, different clustering:
-- folder_items_by_updated: PRIMARY KEY ((folder_id), updated_at, item_type, item_id)
--   CLUSTERING ORDER BY (updated_at DESC, item_type ASC, item_id ASC)
-- folder_items_by_size:    PRIMARY KEY ((folder_id), size_bytes, item_type, item_id)
--   CLUSTERING ORDER BY (size_bytes DESC, item_type ASC, item_id ASC)
```

### `recent_items_by_owner` — owner-scoped, time-series shaped

Uses `TimeWindowCompactionStrategy` — the standard Scylla/Cassandra choice for append-heavy, TTL'd, time-ordered partitions. Avoids tombstone read amplification that a size-tiered strategy would cause here.

```cql
CREATE TABLE IF NOT EXISTS recent_items_by_owner (
  owner_id     uuid,
  event_at     timestamp,
  item_id      uuid,
  item_type    text,       -- 'file' | 'folder'
  folder_id    uuid,
  name         text,
  content_type text,
  size_bytes   bigint,
  is_deleted   boolean,
  PRIMARY KEY ((owner_id), event_at, item_id)
) WITH CLUSTERING ORDER BY (event_at DESC, item_id ASC)
  AND default_time_to_live = 5184000  -- 60 days
  AND compaction = {'class': 'TimeWindowCompactionStrategy', 'compaction_window_unit': 'DAYS', 'compaction_window_size': 1}
  AND gc_grace_seconds = 86400;
```

Follow your existing migration numbering, e.g. `11_folder_items.cql`, `12_recent_items.cql`. `CREATE TABLE IF NOT EXISTS` only — nothing reads or writes these yet, so this step is zero-risk.

---

## 2. Repo layer — `ItemsRepo`

Cassandra/Scylla constraint driving this design: you can't `UPDATE` a clustering-key column in place. A rename changes `name` (clustering key in `folder_items_by_name`), a move changes `folder_id` (partition key everywhere), a resize changes `size_bytes` (clustering key in `folder_items_by_size`). Each needs delete-old + insert-new **specifically in the table where that column sits in the key** — not a blanket delete-everything-then-reinsert.

```go
package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/gocql/gocql"
)

// FolderItem is the denormalized row shared across all folder_items_by_* tables.
type FolderItem struct {
	FolderID     string
	ItemType     string // "file" | "folder"
	ItemID       string
	Name         string
	SizeBytes    int64
	ContentType  string
	ThumbnailKey string
	OwnerID      string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	IsDeleted    bool
	Current      bool
}

type ItemsRepo struct {
	session *gocql.Session
}

func NewItemsRepo(session *gocql.Session) *ItemsRepo {
	return &ItemsRepo{session: session}
}

func upsertQueries(item FolderItem) []struct {
	statement string
	args      []any
} {
	folder, _ := parseID(item.FolderID)
	id, _ := parseID(item.ItemID)
	owner, _ := parseID(item.OwnerID)
	return []struct {
		statement string
		args      []any
	}{
		{`INSERT INTO folder_items_by_name (folder_id, name, item_type, item_id, owner_id, size_bytes, content_type, thumbnail_key, created_at, updated_at, is_deleted, current) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			[]any{folder, item.Name, item.ItemType, id, owner, item.SizeBytes, item.ContentType, item.ThumbnailKey, item.CreatedAt, item.UpdatedAt, item.IsDeleted, item.Current}},
		{`INSERT INTO folder_items_by_updated (folder_id, updated_at, item_type, item_id, owner_id, name, size_bytes, content_type, thumbnail_key, created_at, is_deleted, current) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			[]any{folder, item.UpdatedAt, item.ItemType, id, owner, item.Name, item.SizeBytes, item.ContentType, item.ThumbnailKey, item.CreatedAt, item.IsDeleted, item.Current}},
		{`INSERT INTO folder_items_by_size (folder_id, size_bytes, item_type, item_id, owner_id, name, content_type, thumbnail_key, created_at, updated_at, is_deleted, current) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			[]any{folder, item.SizeBytes, item.ItemType, id, owner, item.Name, item.ContentType, item.ThumbnailKey, item.CreatedAt, item.UpdatedAt, item.IsDeleted, item.Current}},
	}
}

// UpsertItem writes a brand-new item into all three sort tables (no prior row to clean up).
func (r *ItemsRepo) UpsertItem(ctx context.Context, item FolderItem) error {
	for _, q := range upsertQueries(item) {
		if err := r.session.Query(q.statement, q.args...).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("upsert folder item: %w", err)
		}
	}
	return nil
}

// ReplaceItem handles rename/move/resize: insert the new row shape everywhere, then delete
// the old row ONLY in the tables where the clustering key actually changed. prev and next
// can differ in FolderID (move), Name (rename), UpdatedAt (any mutation), or SizeBytes.
func (r *ItemsRepo) ReplaceItem(ctx context.Context, prev, next FolderItem) error {
	for _, q := range upsertQueries(next) {
		if err := r.session.Query(q.statement, q.args...).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("replace folder item insert: %w", err)
		}
	}
	folder, _ := parseID(prev.FolderID)
	id, _ := parseID(prev.ItemID)

	if prev.FolderID != next.FolderID || prev.Name != next.Name {
		if err := r.session.Query(`DELETE FROM folder_items_by_name WHERE folder_id = ? AND name = ? AND item_type = ? AND item_id = ?`,
			folder, prev.Name, prev.ItemType, id).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("replace item delete (by_name): %w", err)
		}
	}
	if prev.FolderID != next.FolderID || !prev.UpdatedAt.Equal(next.UpdatedAt) {
		if err := r.session.Query(`DELETE FROM folder_items_by_updated WHERE folder_id = ? AND updated_at = ? AND item_type = ? AND item_id = ?`,
			folder, prev.UpdatedAt, prev.ItemType, id).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("replace item delete (by_updated): %w", err)
		}
	}
	if prev.FolderID != next.FolderID || prev.SizeBytes != next.SizeBytes {
		if err := r.session.Query(`DELETE FROM folder_items_by_size WHERE folder_id = ? AND size_bytes = ? AND item_type = ? AND item_id = ?`,
			folder, prev.SizeBytes, prev.ItemType, id).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("replace item delete (by_size): %w", err)
		}
	}
	return nil
}

// DeleteItem removes an item's rows entirely — used on permanent purge, not soft-delete
// (soft-delete is a ReplaceItem call with IsDeleted flipped and UpdatedAt bumped).
func (r *ItemsRepo) DeleteItem(ctx context.Context, item FolderItem) error {
	folder, _ := parseID(item.FolderID)
	id, _ := parseID(item.ItemID)
	for _, q := range []struct {
		statement string
		args      []any
	}{
		{`DELETE FROM folder_items_by_name WHERE folder_id = ? AND name = ? AND item_type = ? AND item_id = ?`, []any{folder, item.Name, item.ItemType, id}},
		{`DELETE FROM folder_items_by_updated WHERE folder_id = ? AND updated_at = ? AND item_type = ? AND item_id = ?`, []any{folder, item.UpdatedAt, item.ItemType, id}},
		{`DELETE FROM folder_items_by_size WHERE folder_id = ? AND size_bytes = ? AND item_type = ? AND item_id = ?`, []any{folder, item.SizeBytes, item.ItemType, id}},
	} {
		if err := r.session.Query(q.statement, q.args...).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("delete folder item: %w", err)
		}
	}
	return nil
}
```

`RecentItemsRepo` follows the same `Upsert`/dedup-on-read shape against `recent_items_by_owner` (single table, so no per-column delete branching needed — a `RecordEvent` insert per access/mutation is sufficient, dedup by `item_id` keeping the newest `event_at` happens at read time).

---

## 3. Dual-write wiring

Call `ItemsRepo` / `RecentItemsRepo` from every existing mutation path, **in addition to** existing writes:

- `CreateFile`
- `RenameFileOwned`
- `MoveFileOwned`
- `DeleteFileOwned`
- `RestoreFileOwned`
- `CreateChildFolder`
- `RenameFolderOwned`
- `MoveFolderOwned`
- `cascadeFolderDelete`
- `RestoreFolderOwned`

Example — `MoveFileOwned` becomes: build `prev` from the current row, build `next` with the new `FolderID`/`UpdatedAt`, call `itemsRepo.ReplaceItem(ctx, prev, next)` alongside the existing `metadataRepo.MoveFile` call.

Safe to deploy alone: reads still come from the old paths, so a bug here can only leave the new tables slightly wrong (fixed by backfill), not break production.

---

## 4. Backfill

One-off job scanning `files_by_id` / `folders_by_id`, calling `ItemsRepo.UpsertItem` / `RecentItemsRepo.RecordEvent` for every existing row. Needed because dual-write only covers things mutated **after** deploy — pre-existing data won't show up in the new tables otherwise.

---

## 5. Proto additions

```protobuf
message ListFolderItemsRequest {
  string folder_id = 1;
  string page_token = 2;
  int32  page_size = 3;   // server clamps; client value is a hint only
  Sort   sort = 4;        // NAME | UPDATED | SIZE
}
message ListFolderItemsResponse {
  repeated Item items = 1;
  string next_page_token = 2;
}

message ListRecentItemsRequest {
  string page_token = 1;
  int32  page_size = 2;
}
message ListRecentItemsResponse {
  repeated Item items = 1;
  string next_page_token = 2;
}

message RecordFileAccessRequest { string file_id = 1; }
message RecordFileAccessResponse { bool success = 1; }
```

---

## 6. Service + read path

- `ListFolderItemsOwned` — single-cursor read against `folder_items_by_*`, replacing the old dual-cursor `ListFolderContentsOwned`.
- `ListRecentItemsOwned` — reads `recent_items_by_owner`, hydrates against `files_by_id`/`folders_by_id` to drop anything since deleted, dedups by `item_id` keeping the newest `event_at`.

Can deploy behind a feature flag for canary rollout.

---

## 7. gRPC interceptors — deploy before flipping reads live

### Rate limiter (per-user token bucket via `ristretto`, no new infra)

```go
package interceptor

import (
	"context"
	"time"

	"github.com/dgraph-io/ristretto"
	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type RateLimiter struct {
	cache *ristretto.Cache
	rps   float64
	burst int
	ttl   time.Duration
}

func NewRateLimiter(rps float64, burst int, ttl time.Duration) (*RateLimiter, error) {
	cache, err := ristretto.NewCache(&ristretto.Config{NumCounters: 1e6, MaxCost: 1 << 20, BufferItems: 64})
	if err != nil {
		return nil, err
	}
	return &RateLimiter{cache: cache, rps: rps, burst: burst, ttl: ttl}, nil
}

func (rl *RateLimiter) limiterFor(userID string) *rate.Limiter {
	if v, ok := rl.cache.Get(userID); ok {
		return v.(*rate.Limiter)
	}
	limiter := rate.NewLimiter(rate.Limit(rl.rps), rl.burst)
	rl.cache.SetWithTTL(userID, limiter, 1, rl.ttl) // idle users get evicted, bucket doesn't leak
	return limiter
}

func (rl *RateLimiter) UnaryInterceptor(userIDFromCtx func(context.Context) (string, error)) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		userID, err := userIDFromCtx(ctx)
		if err != nil {
			return nil, status.Error(codes.Unauthenticated, "missing user identity")
		}
		if !rl.limiterFor(userID).Allow() {
			return nil, status.Errorf(codes.ResourceExhausted, "rate limit exceeded, slow down")
		}
		return handler(ctx, req)
	}
}
```

```go
grpc.NewServer(
	grpc.ChainUnaryInterceptor(
		rateLimiter.UnaryInterceptor(extractUserID), // reject before touching Scylla
		timeoutInterceptor(3 * time.Second),
	),
)
```

**Caveat:** this is per-instance, not cluster-wide. With 5 replicas a user effectively gets `5×rps`. Treat it as defense-in-depth behind an edge/gateway rate limit (Envoy, API Gateway); a Redis-backed GCRA limiter is the upgrade path at scale.

### Other read-path guardrails (apply in the repo layer)

- Clamp `page_size` server-side to 10–50 — never trust the client number.
- Opaque cursors only (`gocql` `PageState`, base64'd) — never client-constructed.
- Never `ALLOW FILTERING` — both new tables are single-partition-key queries, so this shouldn't be needed.
- Cache the first page in the existing `ristretto` cache — key by `folder_id:sort:pageSize`, short TTL (5–10s) — to absorb tab-switch bursts.
- Context timeout on every repo call (2–3s) so a slow scan can't pile up goroutines under scroll pressure.

---

## 8. Cutover

Flip the `ListFolderContents` RPC handler to call the new unified read path (or flip the feature flag). Keep old `files_by_folder_name/size/updated` reads available as a rollback fallback for a release or two.

---

## 9. Frontend — scroll-driven infinite pagination

`useInfiniteQuery` + `IntersectionObserver` sentinel + virtualization once lists get long. No page numbers, no offsets — cursor-only, matching the backend.

### Hook

```tsx
// hooks/useFolderItems.ts
import { useInfiniteQuery } from '@tanstack/react-query';

export function useFolderItems(folderId: string, sort: 'name' | 'updated' | 'size') {
  return useInfiniteQuery({
    queryKey: ['folder-items', folderId, sort],
    queryFn: async ({ pageParam }) => {
      const res = await fetch(
        `/api/folders/${folderId}/items?cursor=${pageParam ?? ''}&sort=${sort}`
      );
      if (!res.ok) throw new Error('Failed to load items');
      return res.json() as Promise<{ items: Item[]; nextCursor: string | null }>;
    },
    initialPageParam: null as string | null,
    getNextPageParam: (last) => last.nextCursor,
    staleTime: 10_000,
  });
}
```

Same hook shape for Recents, pointed at `/api/files/recent` — no sort selector needed since it's always `event_at DESC`.

### Component

```tsx
// components/FolderView.tsx
function FolderView({ folderId, sort }: Props) {
  const { data, fetchNextPage, hasNextPage, isFetchingNextPage } =
    useFolderItems(folderId, sort);
  const sentinelRef = useRef<HTMLDivElement>(null);
  const items = data?.pages.flatMap((p) => p.items) ?? [];

  useEffect(() => {
    if (!sentinelRef.current) return;
    const observer = new IntersectionObserver(
      ([entry]) => {
        // guard against duplicate fires — IntersectionObserver + these two
        // flags is enough, no manual debounce needed
        if (entry.isIntersecting && hasNextPage && !isFetchingNextPage) {
          fetchNextPage();
        }
      },
      { rootMargin: '400px' } // prefetch before the user hits bottom
    );
    observer.observe(sentinelRef.current);
    return () => observer.disconnect();
  }, [hasNextPage, isFetchingNextPage, fetchNextPage]);

  return (
    <VirtualizedList items={items}>
      <div ref={sentinelRef} style={{ height: 1 }} />
      {isFetchingNextPage && <SkeletonRows count={6} />}
    </VirtualizedList>
  );
}
```

Two details that matter for robustness:
- `rootMargin: '400px'` triggers the next fetch before the sentinel is visible, so the list never visibly stalls while scrolling fast.
- **Virtualize once items pass a few hundred** (`@tanstack/react-virtual`) — infinite scroll without virtualization just means an ever-growing DOM, which is what actually kills the tab, not the network calls.

### API route (BFF)

```ts
// app/api/files/recent/route.ts
import { getSession } from '@/lib/auth';
import { fileServiceClient } from '@/lib/grpc-client';

export async function GET(req: Request) {
  const session = await getSession(req);
  if (!session) return new Response('Unauthorized', { status: 401 });

  const { searchParams } = new URL(req.url);
  const pageToken = searchParams.get('cursor') ?? '';

  const res = await fileServiceClient.listRecentFiles({
    ownerId: session.userId, // never trust a client-supplied owner_id
    pageToken,
    pageSize: 20,
  });

  return Response.json({ files: res.files, nextCursor: res.nextPageToken });
}
```

**Auth boundary — the one rule that matters most:** `owner_id` always comes from the server-side session, never from the request body/query string. The repo layer's existing owner checks (e.g. `result.OwnerID != owner.String()` in `GetFolder`/`GetFileByID`) should be mirrored in the new `ListFolderItemsOwned`/`ListRecentItemsOwned` paths.

---

## 10. Contract

Once the new unified read path has been live and correct for a while, stop dual-writing to whichever old per-type tables (`files_by_folder_name`, `files_by_folder_size`, `files_by_folder_updated`) are now fully superseded, and drop them in a later migration.

---

## Open follow-ups

- `RecordFileAccess` RPC + service method that feeds `recent_items_by_owner` on file opens/previews (not just mutations).
- Backfill job implementation.
- Cluster-wide rate limiting (Redis GCRA) if the per-instance `ristretto` limiter proves insufficient at scale.
