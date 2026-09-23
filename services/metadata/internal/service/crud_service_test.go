package service

import (
	"testing"

	"github.com/Ajay01103/go-dropbox/metadata/internal/repository"
)

// isInSubtree guards MoveFolderOwned against cycle-creating moves: moving a
// folder into itself or into any of its own descendants must be rejected,
// because the destination's ancestor chain contains the folder being moved.
func TestIsInSubtree(t *testing.T) {
	root := repository.Folder{FolderID: "root", AncestorIDs: nil}
	parent := repository.Folder{FolderID: "parent", AncestorIDs: []string{"root"}}
	child := repository.Folder{FolderID: "child", AncestorIDs: []string{"root", "parent"}}

	tests := []struct {
		name        string
		folderID    string
		destination repository.Folder
		want        bool
	}{
		{"folder into itself", "child", child, true},
		{"folder into its own descendant (cycle)", "parent", child, true},
		{"moving child under parent", "child", parent, false},
		{"sibling move", "child", root, false},
		{"move to root", "parent", root, false},
		{"destination ancestor contains mover (cycle)", "root", child, true},
	}

	// The cycle rule: moving X under D is a cycle iff X == D or X appears in
	// D's ancestor chain (i.e. D is inside X's subtree).
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isInSubtree(tt.folderID, tt.destination); got != tt.want {
				t.Errorf("isInSubtree(%q, %s+{%v}) = %v, want %v",
					tt.folderID, tt.destination.FolderID, tt.destination.AncestorIDs, got, tt.want)
			}
		})
	}
}
