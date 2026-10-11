//go:build integration

package db

import (
	"io/fs"
	"path"
	"sort"
	"testing"
)

// postMigrationSkip returns a skip set that hides the named migration file and
// every file that sorts after it.
//
// The migration tests rebuild a keyspace as it looked before a given migration,
// then run the production Migrate call over it. gocqlx's consistency check
// compares the recorded migration names position-by-position against the
// visible file list, so hiding a file in the middle without also hiding
// everything after it trips "inconsistent migrations" on the very boot the test
// exists to prove works.
//
// Deriving the set from the embedded files instead of listing them by hand is
// the point: every newly added migration used to require editing a constant in
// each test, and forgetting meant two red tests in an unrelated package.
func postMigrationSkip(t *testing.T, firstFile string) map[string]bool {
	t.Helper()

	paths, err := fs.Glob(migrationFS, "migrations/*.cql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	names := make([]string, 0, len(paths))
	for _, migrationPath := range paths {
		names = append(names, path.Base(migrationPath))
	}
	sort.Strings(names)

	skip := make(map[string]bool, len(names))
	for _, name := range names {
		if name >= firstFile {
			skip[name] = true
		}
	}
	if len(skip) == 0 {
		t.Fatalf("no migration file sorts at or after %q; the test is hiding nothing", firstFile)
	}
	return skip
}

// skipUnion merges skip sets. Later sets win, which is irrelevant while every
// value is true.
func skipUnion(sets ...map[string]bool) map[string]bool {
	merged := map[string]bool{}
	for _, set := range sets {
		for name := range set {
			merged[name] = true
		}
	}
	return merged
}
