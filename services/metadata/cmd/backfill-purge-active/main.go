// Command backfill-purge-active populates purge_jobs_active from the existing
// purge_jobs_by_state index, mapping legacy states so the B2b sweeper (and
// later the v2 coordinator) understands every row:
//
//	DECREMENTED_WITH_ERRORS -> DECREMENTED   (error detail survives in
//	                                          has_reconciliation_errors, which stays)
//	METADATA_REMOVED        -> COMPLETE      (removeMetadataAndComplete already ran)
//
// In-flight PENDING / DECREMENT_REQUESTED / DECREMENTED / FAILED rows are
// copied as-is. Rows already present in purge_jobs_active are overwritten
// idempotently (same partition key, same values).
//
// Safe to re-run. Run AFTER deploying the binary that creates purge_jobs_active
// (migration 11) and BEFORE enabling the sweeper (B2b).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/gocql/gocql"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/config"
	mdb "github.com/Ajay01103/go-dropbox/metadata/db"
)

// legacyStateMap maps pre-v2 purge states to their v2 equivalents.
var legacyStateMap = map[string]string{
	"DECREMENTED_WITH_ERRORS": "DECREMENTED",
	"METADATA_REMOVED":        "COMPLETE",
}

// mapState returns the v2 state for a legacy state, or the input unchanged.
func mapState(state string) (target string, mapped bool) {
	if target, ok := legacyStateMap[state]; ok {
		return target, true
	}
	return state, false
}

// states lists every state bucket of purge_jobs_by_state to scan.
var states = []string{
	"PENDING", "DECREMENT_REQUESTED", "DECREMENTED",
	"DECREMENTED_WITH_ERRORS", "METADATA_REMOVED", "COMPLETE", "FAILED",
}

func main() {
	dryRun := flag.Bool("dry-run", false, "print planned writes without executing them")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	session, err := mdb.Connect(ctx, mdb.Config{
		Hosts:             cfg.ScyllaHosts,
		Port:              cfg.ScyllaPort,
		Username:          cfg.ScyllaUsername,
		Password:          cfg.ScyllaPassword,
		Consistency:       gocql.LocalQuorum,
		Datacenter:        cfg.ScyllaDatacenter,
		ReplicationFactor: cfg.ScyllaReplicationFactor,
	})
	if err != nil {
		log.Fatalf("connect scylla: %v", err)
	}
	defer session.Close()

	var copied, mapped, writeErrs int
	for _, state := range states {
		iter := session.Query(
			`SELECT job_id, updated_at FROM purge_jobs_by_state WHERE state = ?`, state,
		).WithContext(ctx).Iter()
		var jobID gocql.UUID
		var updatedAt time.Time
		for iter.Scan(&jobID, &updatedAt) {
			target, isLegacy := mapState(state)
			if isLegacy {
				mapped++
			}
			bucket := updatedAt.UTC().Format("2006010215")
			nextAttempt := updatedAt.UTC()

			if *dryRun {
				if isLegacy {
					fmt.Printf("DRY bucket=%s job=%s state %s -> %s\n", bucket, jobID, state, target)
				}
				copied++
				continue
			}
			if err := session.Query(`INSERT INTO purge_jobs_active (bucket, job_id, state, next_attempt_at) VALUES (?, ?, ?, ?)`,
				bucket, jobID, target, nextAttempt).WithContext(ctx).Exec(); err != nil {
				logger.Error("insert purge_jobs_active", zap.String("job", jobID.String()), zap.Error(err))
				writeErrs++
				continue
			}
			if isLegacy {
				// Persist the mapped state on the authoritative row too, so
				// the coordinator's terminal-state switch recognizes it.
				if err := session.Query(`UPDATE purge_jobs SET state = ? WHERE job_id = ?`,
					target, jobID).WithContext(ctx).Exec(); err != nil {
					logger.Error("map legacy state on purge_jobs", zap.String("job", jobID.String()), zap.Error(err))
					writeErrs++
					continue
				}
			}
			copied++
		}
		if err := iter.Close(); err != nil && err != gocql.ErrNotFound {
			log.Fatalf("scan purge_jobs_by_state (%s): %v", state, err)
		}
	}

	logger.Info("backfill complete",
		zap.Int("rows_copied", copied),
		zap.Int("states_mapped", mapped),
		zap.Int("write_errors", writeErrs),
		zap.Bool("dry_run", *dryRun))
	if writeErrs > 0 {
		log.Fatalf("%d rows failed to copy; re-run the command to retry", writeErrs)
	}
}
