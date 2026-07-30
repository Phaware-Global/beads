package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/dberrors"
)

// migrationPassCompleteKey is the local_metadata row recording that, as of the
// stamping binary's "main/ignored" latest versions, this database had no
// migration work outstanding and no pass in flight.
//
// Read that wording precisely — it is what the code guarantees, and it is
// weaker than "a complete pass ran". MigrateUp stamps after any successful
// return, which includes migrateUp's no-work short-circuit, where no pass
// executed at all. That is intentional and sufficient for the fast path (the
// question it answers is "is a lock-free open safe right now?", not "did a pass
// ever run here?"), with one carve-out: stampMigrationPassComplete declines to
// stamp while auxRekeyResumePending reports a pass that died inside the rekey
// tail, since such a database looks work-free to migrationWorkNeeded but has a
// demonstrably unfinished tail.
//
// It exists for MigrateUpWithLock's lock-free fast path. The probe's other
// input (migrationWorkNeeded: cursors at-latest, content-hash columns present,
// custom-statuses/types backfill done) is satisfied MID-PASS by a running
// migration's per-step commits — those are plain autocommitted statements,
// immediately visible to other sessions on a shared sql-server — before that
// pass's rekeyDependencyIDs / rekeyAuxRowIDs tail and final staging commit have
// run. A concurrent prober could therefore otherwise take the lock-free path
// while another process's pass is still rewriting rows.
//
// The sentinel closes that window by ordering: migrateUp DELETEs it before the
// pass's first mutation, and MigrateUp restamps it only after a fully
// successful return, so "sentinel current" is unsatisfiable while any pass is
// in flight — or died mid-flight, which fails safe (the row stays absent and
// every opener queues on the lock until some pass completes).
//
// local_metadata is dolt-ignored: database-local on a shared server (exactly
// the visibility scope the probe reads), never committed or synced. A fresh
// clone or a pre-sentinel database simply lacks the row and pays one locked
// no-op pass to earn it.
const migrationPassCompleteKey = "migration_pass_complete"

// readMigrationPassStamp returns the raw sentinel value, or "" when there is no
// stamp. Strictly read-only. A missing local_metadata table or absent row both
// report "" (not an error): the caller falls through to the locked pass, which
// re-proves currency and restamps.
//
// The raw value is returned rather than a bool so callers can compare two reads
// for equality — see migrationStateCurrent's seqlock-style check, which must
// distinguish "the same stamp throughout" from "a pass ran in between".
func readMigrationPassStamp(ctx context.Context, db DBConn) (string, error) {
	var value string
	err := db.QueryRowContext(ctx,
		"SELECT value FROM local_metadata WHERE `key` = ?",
		migrationPassCompleteKey).Scan(&value)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || dberrors.IsTableNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading migration pass sentinel: %w", err)
	}
	return value, nil
}

// stampSatisfies reports whether a raw sentinel value records a pass at or past
// this binary's latest main and ignored versions. Empty or unparseable is false.
func stampSatisfies(value string) bool {
	var mainV, ignoredV int
	if n, err := fmt.Sscanf(value, "%d/%d", &mainV, &ignoredV); err != nil || n != 2 {
		return false
	}
	// >= not ==: a newer binary's completed pass already satisfies this
	// binary's requirements, so mixed-binary fleets stay monotonic instead of
	// thrashing the sentinel back and forth.
	return mainV >= LatestVersion() && ignoredV >= LatestIgnoredVersion()
}

// migrationPassCompleteCurrent reports whether a pass at or past this binary's
// latest versions is recorded. Strictly read-only.
func migrationPassCompleteCurrent(ctx context.Context, db DBConn) (bool, error) {
	value, err := readMigrationPassStamp(ctx, db)
	if err != nil {
		return false, err
	}
	return stampSatisfies(value), nil
}

// clearMigrationPassComplete revokes the fast path before a migration pass's
// first mutation. It deliberately does NOT create local_metadata: the pass's
// dirty-table guards compare pending migration SQL against the pre-pass dirty
// set, and materializing the (dolt-ignored, hence perpetually untracked) table
// before those guards run would surface it to them on databases that never had
// it (main 0029 references it).
func clearMigrationPassComplete(ctx context.Context, db DBConn) error {
	if _, err := db.ExecContext(ctx,
		"DELETE FROM local_metadata WHERE `key` = ?",
		migrationPassCompleteKey); err != nil && !dberrors.IsTableNotExist(err) {
		return fmt.Errorf("clearing migration pass sentinel: %w", err)
	}
	return nil
}

// ensureMigrationPassComplete stamps the sentinel with this binary's latest
// versions unless an equal-or-newer stamp is already present (a newer binary's
// completed pass must not be downgraded — its stamp already satisfies this
// binary's >= probe). Called only after a fully successful MigrateUp, under
// the migration lock in server mode.
func ensureMigrationPassComplete(ctx context.Context, db DBConn) error {
	current, err := migrationPassCompleteCurrent(ctx, db)
	if err != nil || current {
		return err
	}
	if err := ensureLocalMetadataTable(ctx, db); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx,
		"REPLACE INTO local_metadata (`key`, value) VALUES (?, ?)",
		migrationPassCompleteKey,
		fmt.Sprintf("%d/%d", LatestVersion(), LatestIgnoredVersion())); err != nil {
		return fmt.Errorf("recording migration pass sentinel: %w", err)
	}
	return nil
}

// ensureLocalMetadataTable creates the clone-local metadata table on demand
// with 0029's DDL — its dolt_ignore pattern is committed history, so the rows
// stay clone-local. Shared by the pass sentinel and the aux-rekey crash
// sentinel.
func ensureLocalMetadataTable(ctx context.Context, db DBConn) error {
	if _, err := db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS local_metadata (`key` VARCHAR(255) PRIMARY KEY, value TEXT NOT NULL DEFAULT '')"); err != nil {
		return fmt.Errorf("ensuring local_metadata: %w", err)
	}
	return nil
}
