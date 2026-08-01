package schema

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestMigrationLockNameUsesRawNameWhenBounded(t *testing.T) {
	got := MigrationLockName("testdb_short")
	want := migrationLockPrefix + "testdb_short"
	if got != want {
		t.Fatalf("MigrationLockName() = %q, want %q", got, want)
	}
}

func TestMigrationLockNameHashesLongNames(t *testing.T) {
	dbName := strings.Repeat("a", 64)
	got := MigrationLockName(dbName)
	if len(got) > migrationLockNameMaxLength {
		t.Fatalf("MigrationLockName() length = %d, want <= %d", len(got), migrationLockNameMaxLength)
	}
	if got == migrationLockPrefix+dbName {
		t.Fatalf("MigrationLockName() used over-limit raw name %q", got)
	}
	if got != MigrationLockName(dbName) {
		t.Fatal("MigrationLockName() is not deterministic")
	}
}

func TestIsMigrationLockError(t *testing.T) {
	err := errors.Join(ErrMigrationLockUnavailable, errors.New("timeout"))
	if !IsMigrationLockError(err) {
		t.Fatal("IsMigrationLockError() = false, want true")
	}
}

func TestMigrateUpRunsWithoutAdvisoryLock(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()

	expectOnePendingMigration(t, mock)

	applied, err := MigrateUp(context.Background(), db)
	if err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	if applied != 1 {
		t.Fatalf("MigrateUp() applied = %d, want 1", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestMigrateUpWithLockUsesDatabaseScopedLockOnly(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin mock connection: %v", err)
	}
	defer conn.Close()

	lockName := MigrationLockName("testdb")
	// The lock-free fast path probes first; no sentinel here, so it falls
	// through and the lock is taken exactly as before.
	expectPassSentinelAbsent(mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, migrationLockAcquireTimeoutSeconds).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	expectOnePendingMigration(t, mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	applied, err := MigrateUpWithLock(ctx, conn, "testdb")
	if err != nil {
		t.Fatalf("MigrateUpWithLock() error = %v", err)
	}
	if applied != 1 {
		t.Fatalf("MigrateUpWithLock() applied = %d, want 1", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func expectOnePendingMigration(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()

	latest := LatestVersion()
	latestIgnored := LatestIgnoredVersion()

	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", latest-1)
	// migrateUp revokes the pass-completion sentinel before its first mutation
	// so no concurrent prober can fast-path into a half-finished pass.
	expectPassSentinelRevoke(mock)
	expectDoltStatusRows(mock)
	expectDoltStatusRows(mock)
	// MigrateUp probes the aux-rekey crash sentinel (bd-578h9.16); this
	// mocked world has no local_metadata table, so no crashed pass.
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	// MigrateUp captures the pre-pass main cursor for the aux re-key
	// watershed (bd-578h9.4) before the main migrations run.
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", latest-1)
	mock.ExpectExec("(?s)^CREATE TABLE IF NOT EXISTS schema_migrations").
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectContentHashColumnExists(mock)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", latest-1)
	if latest == 53 {
		// The v53 pre-repair probes the six rig/agent columns on issues and
		// then the local wisp_dependencies table; this mocked world has all
		// issue columns and no local wisp_dependencies table, so no ALTERs follow.
		for _, col := range []string{"hook_bead", "role_bead", "agent_state", "last_activity", "role_type", "rig"} {
			mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.COLUMNS`).
				WithArgs("issues", col).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		}
		mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
			WithArgs("wisp_dependencies").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	}
	mock.ExpectExec("(?s).*").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO schema_migrations (version, content_hash) VALUES (?, ?)")).
		WithArgs(latest, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectScalar(mock, "SELECT COUNT(*) FROM custom_types", "count", 1)
	expectScalar(mock, "SELECT COUNT(*) FROM custom_statuses", "count", 1)
	// rekeyDependencyIDs probes whether each edge table has an id column; this
	// mocked world has no such table, so both probes return 0 and the re-key
	// no-ops without scanning/updating rows.
	expectColumnExists(mock, false)
	expectColumnExists(mock, false)
	// rekeyAuxRowIDs reads the ignored cursor to see whether its clone-local
	// marker is pending; at latest it is not, so the re-key no-ops.
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", latestIgnored)
	mock.ExpectExec(regexp.QuoteMeta("REPLACE INTO dolt_ignore VALUES ('ignored_schema_migrations', true)")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("(?s)^CREATE TABLE IF NOT EXISTS ignored_schema_migrations").
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectContentHashColumnExists(mock)
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", latestIgnored)
	expectDoltStatusRows(mock)
	expectDoltStatusRows(mock)
	mock.ExpectQuery("(?s)SELECT t\\.TABLE_NAME\\s+FROM INFORMATION_SCHEMA\\.TABLES t").
		WillReturnRows(sqlmock.NewRows([]string{"TABLE_NAME"}).AddRow("schema_migrations"))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_ADD('-f', ?)")).
		WithArgs("schema_migrations").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', 'schema: apply migrations')")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	// MigrateUp restamps the sentinel only after the whole pass has returned.
	expectPassSentinelStamp(mock)
}

// TestMigrateUpWithLockSkipsLockWhenCurrent is the point of the fast path: a
// fully-migrated database with a complete-pass sentinel must not touch
// GET_LOCK at all. No lock expectation is registered, so any acquisition
// attempt shows up as an unexpected query.
func TestMigrateUpWithLockSkipsLockWhenCurrent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin mock connection: %v", err)
	}
	defer conn.Close()

	// Seqlock read order: stamp, mutation-derived state, stamp again.
	expectPassSentinelCurrent(mock)
	expectNoMigrationWork(mock)
	expectPassSentinelCurrent(mock)

	applied, err := MigrateUpWithLock(ctx, conn, "testdb")
	if err != nil {
		t.Fatalf("MigrateUpWithLock() error = %v", err)
	}
	if applied != 0 {
		t.Fatalf("MigrateUpWithLock() applied = %d, want 0", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestMigrateUpWithLockLocksWhenSentinelRevokedMidProbe covers the interleaving
// the seqlock exists for: the prober reads a live stamp, another process then
// starts a pass (revoking the stamp before its first mutation) whose own
// per-step commits make migrationWorkNeeded false, and the prober must NOT
// fast-path into that in-flight pass. Reading the stamp a second time is what
// catches it.
func TestMigrateUpWithLockLocksWhenSentinelRevokedMidProbe(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin mock connection: %v", err)
	}
	defer conn.Close()

	expectPassSentinelCurrent(mock) // read 1: stamp present
	expectNoMigrationWork(mock)     // work-needed false — the in-flight pass made it so
	expectPassSentinelAbsent(mock)  // read 2: the pass revoked it
	// Must fall through to the lock; a contended acquisition ends the test.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(MigrationLockName("testdb"), migrationLockAcquireTimeoutSeconds).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(0))

	if _, err := MigrateUpWithLock(ctx, conn, "testdb"); !errors.Is(err, ErrMigrationLockUnavailable) {
		t.Fatalf("MigrateUpWithLock() error = %v, want ErrMigrationLockUnavailable — "+
			"a stamp revoked mid-probe must force the locked path, not a lock-free open into an in-flight pass", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestMigrateUpSucceedsWhenSentinelWriteFails pins the best-effort contract:
// the sentinel is a performance optimization, so a refused clone-local write
// (Dolt read-only mode, or a bd user without CREATE) must not turn a
// zero-work open into a hard, non-self-healing failure.
func TestMigrateUpSucceedsWhenSentinelWriteFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()

	expectNoMigrationWork(mock) // migrateUp short-circuits: nothing to do
	// Stamp path: resume check (table present, no marker), stamp probe,
	// table-exists probe, then a write the server refuses.
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM local_metadata WHERE `key` = ?")).
		WithArgs(auxRowRekeyInProgressKey).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	expectPassSentinelAbsent(mock)
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec(regexp.QuoteMeta("REPLACE INTO local_metadata")).
		WillReturnError(errors.New("database is read only"))

	applied, err := MigrateUp(context.Background(), db)
	if err != nil {
		t.Fatalf("MigrateUp() error = %v; a failed sentinel write must not fail the open", err)
	}
	if applied != 0 {
		t.Fatalf("MigrateUp() applied = %d, want 0", applied)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestMigrateUpWithLockLocksWhenPassSentinelStale proves the sentinel is
// version-aware: a stamp from an older binary must not satisfy this one, so
// the open falls through to the lock.
func TestMigrateUpWithLockLocksWhenPassSentinelStale(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin mock connection: %v", err)
	}
	defer conn.Close()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM local_metadata WHERE `key` = ?")).
		WithArgs(migrationPassCompleteKey).
		WillReturnRows(sqlmock.NewRows([]string{"value"}).
			AddRow(fmt.Sprintf("%d/%d", LatestVersion()-1, LatestIgnoredVersion())))
	// Falls through to the lock; a contended acquisition ends the test early.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(MigrationLockName("testdb"), migrationLockAcquireTimeoutSeconds).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(0))

	if _, err := MigrateUpWithLock(ctx, conn, "testdb"); !errors.Is(err, ErrMigrationLockUnavailable) {
		t.Fatalf("MigrateUpWithLock() error = %v, want ErrMigrationLockUnavailable (proving it tried to lock)", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestMigrateUpWithLockFallsThroughOnProbeError pins the deliberate choice to
// treat a probe failure as "not current" rather than as an error: the locked
// path is the authority, and a transient read failure must not turn a healthy
// open into a hard failure.
func TestMigrateUpWithLockFallsThroughOnProbeError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin mock connection: %v", err)
	}
	defer conn.Close()

	probeErr := errors.New("transient read failure")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM local_metadata WHERE `key` = ?")).
		WithArgs(migrationPassCompleteKey).
		WillReturnError(probeErr)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(MigrationLockName("testdb"), migrationLockAcquireTimeoutSeconds).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(0))

	_, err = MigrateUpWithLock(ctx, conn, "testdb")
	if errors.Is(err, probeErr) {
		t.Fatalf("MigrateUpWithLock() surfaced the probe error %v; it must fall through to the lock instead", err)
	}
	if !errors.Is(err, ErrMigrationLockUnavailable) {
		t.Fatalf("MigrateUpWithLock() error = %v, want ErrMigrationLockUnavailable", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestMigrationPassCompleteCurrentRejectsUnusableStamps(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"unparseable", "garbage"},
		{"missing ignored half", fmt.Sprintf("%d", LatestVersion())},
		{"main behind", fmt.Sprintf("%d/%d", LatestVersion()-1, LatestIgnoredVersion())},
		{"ignored behind", fmt.Sprintf("%d/%d", LatestVersion(), LatestIgnoredVersion()-1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("create sql mock: %v", err)
			}
			defer db.Close()
			mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM local_metadata WHERE `key` = ?")).
				WithArgs(migrationPassCompleteKey).
				WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(tc.value))
			current, err := migrationPassCompleteCurrent(context.Background(), db)
			if err != nil {
				t.Fatalf("migrationPassCompleteCurrent() error = %v, want nil", err)
			}
			if current {
				t.Fatalf("migrationPassCompleteCurrent() = true for %q, want false", tc.value)
			}
		})
	}
}

// A stamp from a NEWER binary must still satisfy this one, so mixed-binary
// fleets stay monotonic instead of thrashing the sentinel.
func TestMigrationPassCompleteCurrentAcceptsNewerStamp(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM local_metadata WHERE `key` = ?")).
		WithArgs(migrationPassCompleteKey).
		WillReturnRows(sqlmock.NewRows([]string{"value"}).
			AddRow(fmt.Sprintf("%d/%d", LatestVersion()+5, LatestIgnoredVersion()+5)))
	current, err := migrationPassCompleteCurrent(context.Background(), db)
	if err != nil {
		t.Fatalf("migrationPassCompleteCurrent() error = %v", err)
	}
	if !current {
		t.Fatal("migrationPassCompleteCurrent() = false for a newer stamp, want true")
	}
}

// expectPassSentinelCurrent matches a probe finding a complete-pass stamp at
// this binary's versions.
func expectPassSentinelCurrent(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM local_metadata WHERE `key` = ?")).
		WithArgs(migrationPassCompleteKey).
		WillReturnRows(sqlmock.NewRows([]string{"value"}).
			AddRow(fmt.Sprintf("%d/%d", LatestVersion(), LatestIgnoredVersion())))
}

// expectNoMigrationWork matches migrationWorkNeeded finding a fully current
// database: both cursors at latest, both content-hash columns present, and the
// custom statuses/types backfill already done.
func expectNoMigrationWork(mock sqlmock.Sqlmock) {
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations", "version", LatestVersion())
	expectScalar(mock, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations", "version", LatestIgnoredVersion())
	expectContentHashColumnExists(mock)
	expectContentHashColumnExists(mock)
	expectScalar(mock, "SELECT COUNT(*) FROM custom_types", "count", 1)
	expectScalar(mock, "SELECT COUNT(*) FROM custom_statuses", "count", 1)
}

// expectPassSentinelAbsent matches MigrateUpWithLock's pre-lock currency probe
// finding no sentinel row, so the open falls through to the locked pass.
func expectPassSentinelAbsent(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM local_metadata WHERE `key` = ?")).
		WithArgs(migrationPassCompleteKey).
		WillReturnRows(sqlmock.NewRows([]string{"value"}))
}

// expectPassSentinelRevoke matches the sentinel DELETE migrateUp issues before
// the pass's first mutation.
func expectPassSentinelRevoke(mock sqlmock.Sqlmock) {
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM local_metadata WHERE `key` = ?")).
		WithArgs(migrationPassCompleteKey).
		WillReturnResult(sqlmock.NewResult(0, 0))
}

// expectPassSentinelStamp matches the post-pass stamp: the aux-rekey resume
// check (no local_metadata table here, so no crashed pass), then probe
// (absent), create the clone-local table on demand, write the row.
// Against a database that already has local_metadata: aux-rekey resume check
// (table present, no marker -> no crashed pass), stamp probe (absent),
// table-exists probe, write. There is deliberately NO CREATE TABLE — the stamp
// never materializes local_metadata, mirroring clearMigrationPassComplete.
func expectPassSentinelStamp(mock sqlmock.Sqlmock) {
	// auxRekeyResumePending
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM local_metadata WHERE `key` = ?")).
		WithArgs(auxRowRekeyInProgressKey).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	// ensureMigrationPassComplete
	expectPassSentinelAbsent(mock)
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec(regexp.QuoteMeta("REPLACE INTO local_metadata (`key`, value) VALUES (?, ?)")).
		WithArgs(migrationPassCompleteKey, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func expectColumnExists(mock sqlmock.Sqlmock, present bool) {
	n := 0
	if present {
		n = 1
	}
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.COLUMNS`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(n))
}

// expectContentHashColumnExists mocks the idempotent ensureContentHashColumn
// probe, reporting that the content_hash column already exists (so no ALTER runs).
func expectContentHashColumnExists(mock sqlmock.Sqlmock) {
	expectColumnExists(mock, true)
}

func expectScalar(mock sqlmock.Sqlmock, query, column string, value any) {
	mock.ExpectQuery(regexp.QuoteMeta(query)).
		WillReturnRows(sqlmock.NewRows([]string{column}).AddRow(value))
}

func expectDoltStatusRows(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("(?s)SELECT s\\.table_name, s\\.staged\\s+FROM dolt_status s").
		WillReturnRows(sqlmock.NewRows([]string{"table_name", "staged"}))
}

// TestStampDoesNotMaterializeLocalMetadata pins the rule that the stamp never
// creates local_metadata. Materializing this dolt-ignored table on a path that
// previously issued zero writes surfaces it to migrateUp's dirty-table guards,
// which is a hard, non-self-healing error.
func TestStampDoesNotMaterializeLocalMetadata(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	expectNoMigrationWork(mock)
	// auxRekeyResumePending: no local_metadata table at all.
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	expectPassSentinelAbsent(mock)
	// localMetadataExists: still absent -> must stop here.
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	// No CREATE and no REPLACE are registered: either would be an unexpected call.

	if _, err := MigrateUp(context.Background(), db); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// The stamp is best-effort: a refused write must never fail the open, and each
// distinct condition must be logged at most once per process — bounded output
// without permanently silencing a different, later condition.
func TestSentinelLoggingIsCappedPerMessage(t *testing.T) {
	resetSentinelLogged()
	t.Cleanup(resetSentinelLogged)

	var buf bytes.Buffer
	oldOut, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldOut); log.SetFlags(oldFlags) })

	logSentinelOnce("first condition: %v", errors.New("a"))
	logSentinelOnce("first condition: %v", errors.New("b"))
	logSentinelOnce("second condition: %v", errors.New("c"))
	logSentinelOnce("second condition: %v", errors.New("d"))

	lines := 0
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.TrimSpace(l) != "" {
			lines++
		}
	}
	if lines != 2 {
		t.Fatalf("log lines = %d, want 2 (one per distinct message); output:\n%s", lines, buf.String())
	}
	if !strings.Contains(buf.String(), "second condition") {
		t.Fatal("a later distinct condition was silenced; the cap must be per-message, not process-global")
	}
}

// Call-site coverage for the log cap: replacing logSentinelOnce with a bare
// log.Printf in stampMigrationPassComplete must not go unnoticed. Two opens
// against a persistently unstampable database may emit at most one line.
func TestStampFailureLogsOncePerProcessAcrossOpens(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	resetSentinelLogged()
	t.Cleanup(resetSentinelLogged)

	var buf bytes.Buffer
	oldOut, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldOut); log.SetFlags(oldFlags) })

	for i := 0; i < 2; i++ {
		expectNoMigrationWork(mock)
		mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM local_metadata WHERE `key` = ?")).
			WithArgs(auxRowRekeyInProgressKey).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		expectPassSentinelAbsent(mock)
		mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectExec(regexp.QuoteMeta("REPLACE INTO local_metadata")).
			WillReturnError(errors.New("database is read only"))
	}

	for i := 0; i < 2; i++ {
		if _, err := MigrateUp(context.Background(), db); err != nil {
			t.Fatalf("MigrateUp() call %d error = %v", i+1, err)
		}
	}

	lines := 0
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.Contains(l, "migration pass sentinel") {
			lines++
		}
	}
	if lines != 1 {
		t.Fatalf("sentinel log lines across 2 opens = %d, want 1; the cap must hold at the call site.\nlog:\n%s", lines, buf.String())
	}
}

// countingConn wraps a DBConn and counts ExecContext calls matching a
// substring, so a test can assert that a write did NOT happen. go-sqlmock
// cannot: ExpectationsWereMet reports only UNFULFILLED expectations and never
// records unexpected calls, and MigrateUp swallows stamp errors by design.
type countingConn struct {
	DBConn
	match string
	n     int
}

func (c *countingConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, c.match) {
		c.n++
	}
	return c.DBConn.ExecContext(ctx, query, args...)
}

// The aux-probe branch of stampMigrationPassComplete has its own logSentinelOnce
// call site. Without this, that site could be reverted to a bare log.Printf with
// the whole package staying green — restoring per-open unbounded stderr on
// exactly the deployments where the local_metadata read is persistently refused.
func TestAuxProbeFailureLogsOncePerProcess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	resetSentinelLogged()
	t.Cleanup(resetSentinelLogged)

	var buf bytes.Buffer
	oldOut, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldOut); log.SetFlags(oldFlags) })

	// Two opens where the aux-rekey probe itself fails.
	for i := 0; i < 2; i++ {
		expectNoMigrationWork(mock)
		mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
			WillReturnError(errors.New("permission denied reading catalog"))
	}

	for i := 0; i < 2; i++ {
		if _, err := MigrateUp(context.Background(), db); err != nil {
			t.Fatalf("MigrateUp() call %d error = %v", i+1, err)
		}
	}

	lines := 0
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.Contains(l, "aux-rekey resume state") {
			lines++
		}
	}
	if lines != 1 {
		t.Fatalf("aux-probe log lines across 2 opens = %d, want 1; the cap must hold at this call site too.\nlog:\n%s", lines, buf.String())
	}
}

// The '|| resuming' carve-out: a database whose aux rekey tail died mid-flight
// must NOT be stamped, because migrateUp's no-work short-circuit reports success
// for it without executing that tail. Asserted by counting writes — dropping the
// carve-out otherwise leaves the package green.
func TestResumingAuxRekeyIsNotStamped(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	resetSentinelLogged()
	t.Cleanup(resetSentinelLogged)

	// Unordered, and the REST of the stamp path is mocked to SUCCEED. That is
	// what gives this test teeth: if the carve-out is dropped, the stamp runs to
	// completion and issues the REPLACE, which the counter sees. Mocking only up
	// to the marker would make the un-carved code merely error out on a missing
	// expectation, and the swallowed error would look identical to success.
	mock.MatchExpectationsInOrder(false)
	expectNoMigrationWork(mock)
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM local_metadata WHERE `key` = ?")).
		WithArgs(auxRowRekeyInProgressKey).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1)) // resuming
	expectPassSentinelAbsent(mock)
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec(regexp.QuoteMeta("REPLACE INTO local_metadata")).
		WillReturnResult(sqlmock.NewResult(0, 1))

	conn := &countingConn{DBConn: db, match: "REPLACE INTO local_metadata"}
	if _, err := MigrateUp(context.Background(), conn); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	if conn.n != 0 {
		t.Fatalf("stamp writes = %d, want 0; a database whose aux-rekey tail died mid-flight must not be certified", conn.n)
	}
}

func TestStampSatisfiesRejectsTrailingGarbage(t *testing.T) {
	good := fmt.Sprintf("%d/%d", LatestVersion(), LatestIgnoredVersion())
	if !stampSatisfies(good) {
		t.Fatalf("stampSatisfies(%q) = false, want true", good)
	}
	for _, bad := range []string{
		" " + good,    // leading whitespace
		good + "\n",   // trailing newline
		good + "junk", // trailing bytes
		good + "/7",   // extra separator
	} {
		if stampSatisfies(bad) {
			t.Fatalf("stampSatisfies(%q) = true; a corrupted stamp must not certify the fast path", bad)
		}
	}
}
