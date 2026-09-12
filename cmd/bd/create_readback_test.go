package main

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

// fakeDroppedWriteStore satisfies storage.DoltStorage via an embedded nil
// interface (any unimplemented method panics) and returns a configurable
// readback result, simulating a create whose write never landed.
type fakeDroppedWriteStore struct {
	storage.DoltStorage
	readable []*types.Issue
	closed   bool
}

func (f *fakeDroppedWriteStore) GetIssuesByIDs(_ context.Context, _ []string) ([]*types.Issue, error) {
	return f.readable, nil
}

func (f *fakeDroppedWriteStore) Close() error {
	f.closed = true
	return nil
}

// withReadbackStoreOpener swaps readbackStoreOpener for the duration of a
// test so verifyIssuesReadable's fresh-open call resolves to a fake store
// instead of touching real Dolt storage, and restores the original after.
func withReadbackStoreOpener(t *testing.T, open func(ctx context.Context, beadsDir string) (storage.DoltStorage, error)) {
	t.Helper()
	orig := readbackStoreOpener
	readbackStoreOpener = open
	t.Cleanup(func() { readbackStoreOpener = orig })
}

// TestVerifyIssuesReadableDetectsDroppedWrite is the RED/GREEN detector test
// for hq-dzmd0: bd create must not report success unless the bead it claims
// to have created is subsequently readable. It simulates the failure mode
// directly — CreateIssue returned no error, but the issue is not readable
// back — rather than exercising the normal create path, which passes both
// before and after this fix and certifies nothing.
func TestVerifyIssuesReadableDetectsDroppedWrite(t *testing.T) {
	fake := &fakeDroppedWriteStore{readable: nil} // dropped write: nothing comes back
	withReadbackStoreOpener(t, func(context.Context, string) (storage.DoltStorage, error) {
		return fake, nil
	})

	err := verifyIssuesReadable(context.Background(), "/configured/beads-dir", []string{"bd-dropped"})
	if err == nil {
		t.Fatal("verifyIssuesReadable: expected an error for a write reported successful but not readable back from storage, got nil")
	}
	if !strings.Contains(err.Error(), "bd-dropped") {
		t.Fatalf("verifyIssuesReadable: error should name the missing issue id, got: %v", err)
	}
	if !fake.closed {
		t.Error("verifyIssuesReadable: expected the freshly-opened store to be closed")
	}
}

func TestVerifyIssuesReadableSucceedsWhenReadable(t *testing.T) {
	fake := &fakeDroppedWriteStore{readable: []*types.Issue{{ID: "bd-ok"}}}
	withReadbackStoreOpener(t, func(context.Context, string) (storage.DoltStorage, error) {
		return fake, nil
	})

	if err := verifyIssuesReadable(context.Background(), "/configured/beads-dir", []string{"bd-ok"}); err != nil {
		t.Fatalf("verifyIssuesReadable: unexpected error for a readable issue: %v", err)
	}
}

// TestVerifyIssuesReadableDetectsMisroutedWrite is the test bd-quc0 requires:
// one where the write LANDS SOMEWHERE ELSE and the readback must still
// report failure, distinguishing this fix from
// TestVerifyIssuesReadableDetectsDroppedWrite (which only covers a write that
// vanishes entirely — a case the pre-fix implementation already handled).
//
// It simulates a misroute by making the two stores diverge: `ambientHandle`
// stands in for the store instance that performed the write (and would still
// be sitting in the `store`/`s` local variable at the call site) and DOES
// have the bead — because the write landed there, just not where it was
// configured to. `configuredDestination` stands in for a fresh store opened
// straight from the target beadsDir's own metadata.json, and does NOT have
// the bead, because the real configured destination never received it.
//
// Before bd-quc0, verifyIssuesReadable took the ambient handle as a
// parameter and read back through it — so this exact scenario would have
// reported success (RED: the ambient handle has the bead). After bd-quc0,
// verifyIssuesReadable no longer accepts the ambient handle at all; it only
// ever reads through readbackStoreOpener, which this test points at
// configuredDestination — so it correctly reports failure (GREEN).
func TestVerifyIssuesReadableDetectsMisroutedWrite(t *testing.T) {
	ambientHandle := &fakeDroppedWriteStore{readable: []*types.Issue{{ID: "bd-misrouted"}}}
	configuredDestination := &fakeDroppedWriteStore{readable: nil}

	var openedBeadsDir string
	withReadbackStoreOpener(t, func(_ context.Context, beadsDir string) (storage.DoltStorage, error) {
		openedBeadsDir = beadsDir
		return configuredDestination, nil // the fix must open THIS store, not ambientHandle
	})

	err := verifyIssuesReadable(context.Background(), "/configured/beads-dir", []string{"bd-misrouted"})
	if err == nil {
		t.Fatal("verifyIssuesReadable: expected an error for a write that landed outside the configured destination, got nil (readback is following the misroute)")
	}
	if !strings.Contains(err.Error(), "bd-misrouted") {
		t.Fatalf("verifyIssuesReadable: error should name the misrouted issue id, got: %v", err)
	}
	if openedBeadsDir != "/configured/beads-dir" {
		t.Fatalf("verifyIssuesReadable: expected readback to open the configured beadsDir %q, got %q", "/configured/beads-dir", openedBeadsDir)
	}
	if ambientHandle.readable == nil {
		t.Fatal("test setup error: ambientHandle should still 'have' the bead to prove the fix isn't reading it")
	}
}

// fakeReadbackIssueUseCase implements only the two IssueUseCase methods the
// proxied-server readback check calls; every other method panics via the
// embedded nil interface if invoked.
type fakeReadbackIssueUseCase struct {
	domain.IssueUseCase
	issuesFound []*types.Issue
	wispsFound  []*types.Issue
}

func (f *fakeReadbackIssueUseCase) GetIssuesByIDs(_ context.Context, _ []string) ([]*types.Issue, error) {
	return f.issuesFound, nil
}

func (f *fakeReadbackIssueUseCase) GetWispsByIDs(_ context.Context, _ []string) ([]*types.Issue, error) {
	return f.wispsFound, nil
}

type fakeReadbackUOW struct {
	uow.UnitOfWork
	issueUseCase domain.IssueUseCase
}

func (f *fakeReadbackUOW) IssueUseCase() domain.IssueUseCase { return f.issueUseCase }
func (f *fakeReadbackUOW) Close(context.Context)             {}

type fakeReadbackUOWProvider struct {
	uw uow.UnitOfWork
}

func (p *fakeReadbackUOWProvider) NewUOW(context.Context) (uow.UnitOfWork, error) { return p.uw, nil }
func (p *fakeReadbackUOWProvider) Close(context.Context) error                    { return nil }

// TestVerifyIssuesReadableProxiedDetectsDroppedWrite mirrors
// TestVerifyIssuesReadableDetectsDroppedWrite for the proxied-server create
// path (cmd/bd/create_proxied_server.go), whose success sites read back
// through a freshly opened unit of work rather than storage.DoltStorage.
func TestVerifyIssuesReadableProxiedDetectsDroppedWrite(t *testing.T) {
	origProvider := uowProvider
	defer func() { uowProvider = origProvider }()

	fakeUC := &fakeReadbackIssueUseCase{issuesFound: nil} // dropped write
	uowProvider = &fakeReadbackUOWProvider{uw: &fakeReadbackUOW{issueUseCase: fakeUC}}

	err := verifyIssuesReadableProxied(context.Background(), []string{"bd-dropped"}, false)
	if err == nil {
		t.Fatal("verifyIssuesReadableProxied: expected an error for a write reported successful but not readable back from storage, got nil")
	}
	if !strings.Contains(err.Error(), "bd-dropped") {
		t.Fatalf("verifyIssuesReadableProxied: error should name the missing issue id, got: %v", err)
	}
}

func TestVerifyIssuesReadableProxiedDetectsDroppedWispWrite(t *testing.T) {
	origProvider := uowProvider
	defer func() { uowProvider = origProvider }()

	fakeUC := &fakeReadbackIssueUseCase{wispsFound: nil} // dropped write
	uowProvider = &fakeReadbackUOWProvider{uw: &fakeReadbackUOW{issueUseCase: fakeUC}}

	err := verifyIssuesReadableProxied(context.Background(), []string{"bd-wisp-dropped"}, true)
	if err == nil {
		t.Fatal("verifyIssuesReadableProxied: expected an error for a dropped ephemeral wisp write, got nil")
	}
	if !strings.Contains(err.Error(), "bd-wisp-dropped") {
		t.Fatalf("verifyIssuesReadableProxied: error should name the missing wisp id, got: %v", err)
	}
}

func TestVerifyIssuesReadableProxiedSucceedsWhenReadable(t *testing.T) {
	origProvider := uowProvider
	defer func() { uowProvider = origProvider }()

	fakeUC := &fakeReadbackIssueUseCase{issuesFound: []*types.Issue{{ID: "bd-ok"}}}
	uowProvider = &fakeReadbackUOWProvider{uw: &fakeReadbackUOW{issueUseCase: fakeUC}}

	if err := verifyIssuesReadableProxied(context.Background(), []string{"bd-ok"}, false); err != nil {
		t.Fatalf("verifyIssuesReadableProxied: unexpected error for a readable issue: %v", err)
	}
}
