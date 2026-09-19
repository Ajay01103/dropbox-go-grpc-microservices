package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/gocql/gocql"
)

const (
	defaultItemsPageSize = 20
	maxItemsPageSize     = 100
)

// FolderItem is the denormalized row shared by the folder item sort tables.
type FolderItem struct {
	FolderID     string
	ItemType     string // "file" or "folder"
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

// ItemsRepo provides access to the merged file and folder indexes.
type ItemsRepo struct {
	session *gocql.Session
}

// NewItemsRepo creates an ItemsRepo backed by a ScyllaDB session.
func NewItemsRepo(session *gocql.Session) *ItemsRepo {
	return &ItemsRepo{session: session}
}

type itemQuery struct {
	statement string
	args      []any
}

func folderItemQueries(item FolderItem) ([]itemQuery, error) {
	folder, err := parseID(item.FolderID)
	if err != nil {
		return nil, fmt.Errorf("invalid folder item folder id: %w", err)
	}
	id, err := parseID(item.ItemID)
	if err != nil {
		return nil, fmt.Errorf("invalid folder item id: %w", err)
	}
	owner, err := parseID(item.OwnerID)
	if err != nil {
		return nil, fmt.Errorf("invalid folder item owner id: %w", err)
	}

	return []itemQuery{
		{
			statement: `INSERT INTO folder_items_by_name (folder_id, name, item_type, item_id, owner_id, size_bytes, content_type, thumbnail_key, created_at, updated_at, is_deleted, current) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			args:      []any{folder, item.Name, item.ItemType, id, owner, item.SizeBytes, item.ContentType, item.ThumbnailKey, item.CreatedAt, item.UpdatedAt, item.IsDeleted, item.Current},
		},
		{
			statement: `INSERT INTO folder_items_by_updated (folder_id, updated_at, item_type, item_id, owner_id, name, size_bytes, content_type, thumbnail_key, created_at, is_deleted, current) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			args:      []any{folder, item.UpdatedAt, item.ItemType, id, owner, item.Name, item.SizeBytes, item.ContentType, item.ThumbnailKey, item.CreatedAt, item.IsDeleted, item.Current},
		},
		{
			statement: `INSERT INTO folder_items_by_size (folder_id, size_bytes, item_type, item_id, owner_id, name, content_type, thumbnail_key, created_at, updated_at, is_deleted, current) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			args:      []any{folder, item.SizeBytes, item.ItemType, id, owner, item.Name, item.ContentType, item.ThumbnailKey, item.CreatedAt, item.UpdatedAt, item.IsDeleted, item.Current},
		},
	}, nil
}

// UpsertItem writes a new item to all three folder item indexes.
func (r *ItemsRepo) UpsertItem(ctx context.Context, item FolderItem) error {
	queries, err := folderItemQueries(item)
	if err != nil {
		return err
	}
	for _, query := range queries {
		if err := r.session.Query(query.statement, query.args...).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("upsert folder item: %w", err)
		}
	}
	return nil
}

// ReplaceItem writes next and removes prev wherever a partition or clustering
// key changed. Cassandra cannot update those key columns in place.
func (r *ItemsRepo) ReplaceItem(ctx context.Context, prev, next FolderItem) error {
	if err := r.UpsertItem(ctx, next); err != nil {
		return fmt.Errorf("replace folder item insert: %w", err)
	}

	folder, err := parseID(prev.FolderID)
	if err != nil {
		return fmt.Errorf("invalid previous folder item folder id: %w", err)
	}
	id, err := parseID(prev.ItemID)
	if err != nil {
		return fmt.Errorf("invalid previous folder item id: %w", err)
	}
	typeChanged := prev.ItemType != next.ItemType
	folderChanged := prev.FolderID != next.FolderID

	if folderChanged || prev.Name != next.Name || typeChanged {
		if err := r.session.Query(
			`DELETE FROM folder_items_by_name WHERE folder_id = ? AND name = ? AND item_type = ? AND item_id = ?`,
			folder, prev.Name, prev.ItemType, id,
		).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("replace item delete by name: %w", err)
		}
	}
	if folderChanged || !prev.UpdatedAt.Equal(next.UpdatedAt) || typeChanged {
		if err := r.session.Query(
			`DELETE FROM folder_items_by_updated WHERE folder_id = ? AND updated_at = ? AND item_type = ? AND item_id = ?`,
			folder, prev.UpdatedAt, prev.ItemType, id,
		).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("replace item delete by updated: %w", err)
		}
	}
	if folderChanged || prev.SizeBytes != next.SizeBytes || typeChanged {
		if err := r.session.Query(
			`DELETE FROM folder_items_by_size WHERE folder_id = ? AND size_bytes = ? AND item_type = ? AND item_id = ?`,
			folder, prev.SizeBytes, prev.ItemType, id,
		).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("replace item delete by size: %w", err)
		}
	}
	return nil
}

// DeleteItem removes an item from all folder item indexes.
func (r *ItemsRepo) DeleteItem(ctx context.Context, item FolderItem) error {
	folder, err := parseID(item.FolderID)
	if err != nil {
		return fmt.Errorf("invalid folder item folder id: %w", err)
	}
	id, err := parseID(item.ItemID)
	if err != nil {
		return fmt.Errorf("invalid folder item id: %w", err)
	}
	queries := []itemQuery{
		{statement: `DELETE FROM folder_items_by_name WHERE folder_id = ? AND name = ? AND item_type = ? AND item_id = ?`, args: []any{folder, item.Name, item.ItemType, id}},
		{statement: `DELETE FROM folder_items_by_updated WHERE folder_id = ? AND updated_at = ? AND item_type = ? AND item_id = ?`, args: []any{folder, item.UpdatedAt, item.ItemType, id}},
		{statement: `DELETE FROM folder_items_by_size WHERE folder_id = ? AND size_bytes = ? AND item_type = ? AND item_id = ?`, args: []any{folder, item.SizeBytes, item.ItemType, id}},
	}
	for _, query := range queries {
		if err := r.session.Query(query.statement, query.args...).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("delete folder item: %w", err)
		}
	}
	return nil
}

func clampItemsPageSize(pageSize int) int {
	if pageSize <= 0 {
		return defaultItemsPageSize
	}
	if pageSize > maxItemsPageSize {
		return maxItemsPageSize
	}
	return pageSize
}

// ListItems returns one page from the requested sort index. pageToken is an
// opaque token returned by a previous call; an empty sort uses name order.
func (r *ItemsRepo) ListItems(ctx context.Context, folderID, sortBy string, pageSize int, pageToken string) ([]FolderItem, string, error) {
	folder, err := parseID(folderID)
	if err != nil {
		return nil, "", fmt.Errorf("invalid folder id: %w", err)
	}
	state, err := decodePageState(pageToken)
	if err != nil {
		return nil, "", err
	}

	pageSize = clampItemsPageSize(pageSize)
	columns := "folder_id, name, item_type, item_id, owner_id, size_bytes, content_type, thumbnail_key, created_at, updated_at, is_deleted, current"
	table := "folder_items_by_name"
	switch sortBy {
	case "", "name":
	case "updated":
		table = "folder_items_by_updated"
		columns = "folder_id, updated_at, item_type, item_id, owner_id, name, size_bytes, content_type, thumbnail_key, created_at, is_deleted, current"
	case "size":
		table = "folder_items_by_size"
		columns = "folder_id, size_bytes, item_type, item_id, owner_id, name, content_type, thumbnail_key, created_at, updated_at, is_deleted, current"
	default:
		return nil, "", fmt.Errorf("unsupported item sort %q", sortBy)
	}

	iter := r.session.Query(fmt.Sprintf("SELECT %s FROM %s WHERE folder_id = ?", columns, table), folder).
		PageSize(pageSize).PageState(state).WithContext(ctx).Iter()
	items := make([]FolderItem, 0, pageSize)
	for {
		var item FolderItem
		var storedFolder, storedItem, storedOwner gocql.UUID
		var storedItemType string
		var scanned bool
		switch sortBy {
		case "updated":
			var updatedAt time.Time
			scanned = iter.Scan(&storedFolder, &updatedAt, &storedItemType, &storedItem, &storedOwner, &item.Name, &item.SizeBytes, &item.ContentType, &item.ThumbnailKey, &item.CreatedAt, &item.IsDeleted, &item.Current)
			item.UpdatedAt = updatedAt
		case "size":
			var sizeBytes int64
			scanned = iter.Scan(&storedFolder, &sizeBytes, &storedItemType, &storedItem, &storedOwner, &item.Name, &item.ContentType, &item.ThumbnailKey, &item.CreatedAt, &item.UpdatedAt, &item.IsDeleted, &item.Current)
			item.SizeBytes = sizeBytes
		default:
			scanned = iter.Scan(&storedFolder, &item.Name, &storedItemType, &storedItem, &storedOwner, &item.SizeBytes, &item.ContentType, &item.ThumbnailKey, &item.CreatedAt, &item.UpdatedAt, &item.IsDeleted, &item.Current)
		}
		if !scanned {
			break
		}
		item.FolderID = storedFolder.String()
		item.ItemType = storedItemType
		item.ItemID = storedItem.String()
		item.OwnerID = storedOwner.String()
		items = append(items, item)
	}
	if err := iter.Close(); err != nil {
		return nil, "", fmt.Errorf("list folder items: %w", err)
	}
	return items, encodePageState(iter.PageState()), nil
}

// RecentItem is an item access or mutation event stored in the recent index.
type RecentItem struct {
	OwnerID     string
	EventAt     time.Time
	ItemID      string
	ItemType    string
	FolderID    string
	Name        string
	ContentType string
	SizeBytes   int64
	IsDeleted   bool
}

// RecentItemsRepo provides access to the owner-scoped recent item index.
type RecentItemsRepo struct {
	session *gocql.Session
}

// NewRecentItemsRepo creates a RecentItemsRepo backed by a ScyllaDB session.
func NewRecentItemsRepo(session *gocql.Session) *RecentItemsRepo {
	return &RecentItemsRepo{session: session}
}

// DeleteRecentItemEvents removes every event row for the given item from the
// owner's recent-items partition. Because the table's clustering key includes
// event_at (a per-event timestamp) there can be many rows per item_id; we scan
// the partition once and issue individual deletes for each matching row.
// The operation is idempotent — deleting absent rows is a no-op in Cassandra.
func (r *RecentItemsRepo) DeleteRecentItemEvents(ctx context.Context, ownerID, itemID string) error {
	owner, err := parseID(ownerID)
	if err != nil {
		return fmt.Errorf("invalid owner id for recent item delete: %w", err)
	}
	item, err := parseID(itemID)
	if err != nil {
		return fmt.Errorf("invalid item id for recent item delete: %w", err)
	}

	// Collect all event_at timestamps for this item in one pass.
	iter := r.session.Query(
		`SELECT event_at FROM recent_items_by_owner WHERE owner_id = ? AND item_id = ? ALLOW FILTERING`,
		owner, item,
	).WithContext(ctx).Iter()
	defer iter.Close()

	var eventAts []interface{}
	var eventAt time.Time
	for iter.Scan(&eventAt) {
		// Copy the value before appending — iter reuses the variable.
		t := eventAt
		eventAts = append(eventAts, t)
	}
	if err := iter.Close(); err != nil && err.Error() != "not found" {
		return fmt.Errorf("scan recent item events: %w", err)
	}

	for _, ea := range eventAts {
		if err := r.session.Query(
			`DELETE FROM recent_items_by_owner WHERE owner_id = ? AND event_at = ? AND item_id = ?`,
			owner, ea, item,
		).WithContext(ctx).Exec(); err != nil {
			return fmt.Errorf("delete recent item event: %w", err)
		}
	}
	return nil
}

// RecordEvent appends an item event. The table TTL bounds its retention.
func (r *RecentItemsRepo) RecordEvent(ctx context.Context, item RecentItem) error {
	owner, err := parseID(item.OwnerID)
	if err != nil {
		return fmt.Errorf("invalid recent item owner id: %w", err)
	}
	itemID, err := parseID(item.ItemID)
	if err != nil {
		return fmt.Errorf("invalid recent item id: %w", err)
	}
	folder, err := parseID(item.FolderID)
	if err != nil {
		return fmt.Errorf("invalid recent item folder id: %w", err)
	}
	if err := r.session.Query(
		`INSERT INTO recent_items_by_owner (owner_id, event_at, item_id, item_type, folder_id, name, content_type, size_bytes, is_deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		owner, item.EventAt, itemID, item.ItemType, folder, item.Name, item.ContentType, item.SizeBytes, item.IsDeleted,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("record recent item: %w", err)
	}
	return nil
}

// ListRecentItems returns a page of recent events, keeping the newest event
// for each item ID in the page. Cassandra supplies newest events first.
func (r *RecentItemsRepo) ListRecentItems(ctx context.Context, ownerID string, pageSize int, pageToken string) ([]RecentItem, string, error) {
	owner, err := parseID(ownerID)
	if err != nil {
		return nil, "", fmt.Errorf("invalid owner id: %w", err)
	}
	state, err := decodePageState(pageToken)
	if err != nil {
		return nil, "", err
	}
	pageSize = clampItemsPageSize(pageSize)
	iter := r.session.Query(
		`SELECT event_at, item_id, item_type, folder_id, name, content_type, size_bytes, is_deleted FROM recent_items_by_owner WHERE owner_id = ?`,
		owner,
	).PageSize(pageSize).PageState(state).WithContext(ctx).Iter()

	items := make([]RecentItem, 0, pageSize)
	seen := make(map[string]struct{}, pageSize)
	for {
		var item RecentItem
		var itemID, folderID gocql.UUID
		if !iter.Scan(&item.EventAt, &itemID, &item.ItemType, &folderID, &item.Name, &item.ContentType, &item.SizeBytes, &item.IsDeleted) {
			break
		}
		itemIDString := itemID.String()
		if _, exists := seen[itemIDString]; exists {
			continue
		}
		seen[itemIDString] = struct{}{}
		item.OwnerID = owner.String()
		item.ItemID = itemIDString
		item.FolderID = folderID.String()
		items = append(items, item)
	}
	if err := iter.Close(); err != nil {
		return nil, "", fmt.Errorf("list recent items: %w", err)
	}
	return items, encodePageState(iter.PageState()), nil
}
