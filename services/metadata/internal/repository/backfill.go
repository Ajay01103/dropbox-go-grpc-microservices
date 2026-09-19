package repository

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gocql/gocql"
)

type backfillRecord struct {
	item  FolderItem
	event RecentItem
}

// Backfill rebuilds the merged indexes from the authoritative metadata tables.
// Writes are bounded by workers and interval. The operation is idempotent and
// can safely be restarted after cancellation.
func (r *ItemsRepo) Backfill(ctx context.Context, recent *RecentItemsRepo, workers int, interval time.Duration) error {
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan backfillRecord, workers*2)
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	var once sync.Once
	fail := func(err error) { once.Do(func() { errCh <- err }) }

	var limiter <-chan time.Time
	var ticker *time.Ticker
	if interval > 0 {
		ticker = time.NewTicker(interval)
		defer ticker.Stop()
		limiter = ticker.C
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for record := range jobs {
				if limiter != nil {
					select {
					case <-ctx.Done():
						return
					case <-limiter:
					}
				}
				if err := r.UpsertItem(ctx, record.item); err != nil {
					fail(err)
					continue
				}
				if recent != nil {
					if err := recent.RecordEvent(ctx, record.event); err != nil {
						fail(err)
					}
				}
			}
		}()
	}

	enqueue := func(record backfillRecord) bool {
		select {
		case <-ctx.Done():
			return false
		case jobs <- record:
			return true
		}
	}

	files := r.session.Query(`SELECT file_id, folder_id, owner_id, filename, size_bytes, content_type, created_at, updated_at, thumbnail_key, is_deleted, current FROM files_by_id`).WithContext(ctx).Iter()
	for {
		var id, folder, owner gocql.UUID
		var item FolderItem
		if !files.Scan(&id, &folder, &owner, &item.Name, &item.SizeBytes, &item.ContentType, &item.CreatedAt, &item.UpdatedAt, &item.ThumbnailKey, &item.IsDeleted, &item.Current) {
			break
		}
		item.FolderID, item.ItemType, item.ItemID, item.OwnerID = folder.String(), "file", id.String(), owner.String()
		if !enqueue(backfillRecord{item: item, event: recentItemFromFolderItem(item)}) {
			break
		}
	}
	if err := files.Close(); err != nil {
		fail(fmt.Errorf("scan files for backfill: %w", err))
	}

	folders := r.session.Query(`SELECT folder_id, owner_id, parent_id, name, created_at, updated_at, is_deleted FROM folders_by_id`).WithContext(ctx).Iter()
	for {
		var id, owner, parent gocql.UUID
		var item FolderItem
		if !folders.Scan(&id, &owner, &parent, &item.Name, &item.CreatedAt, &item.UpdatedAt, &item.IsDeleted) {
			break
		}
		if parent == (gocql.UUID{}) {
			continue
		}
		item.FolderID, item.ItemType, item.ItemID, item.OwnerID = parent.String(), "folder", id.String(), owner.String()
		item.Current = !item.IsDeleted
		if !enqueue(backfillRecord{item: item, event: recentItemFromFolderItem(item)}) {
			break
		}
	}
	if err := folders.Close(); err != nil {
		fail(fmt.Errorf("scan folders for backfill: %w", err))
	}
	close(jobs)
	wg.Wait()

	select {
	case err := <-errCh:
		return fmt.Errorf("backfill items: %w", err)
	default:
		return ctx.Err()
	}
}

func recentItemFromFolderItem(item FolderItem) RecentItem {
	return RecentItem{OwnerID: item.OwnerID, EventAt: time.Now().UTC(), ItemID: item.ItemID, ItemType: item.ItemType, FolderID: item.FolderID, Name: item.Name, ContentType: item.ContentType, SizeBytes: item.SizeBytes, IsDeleted: item.IsDeleted}
}
