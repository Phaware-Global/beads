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
}

func (f *fakeDroppedWriteStore) GetIssuesByIDs(_ context.Context, _ []string) ([]*types.Issue, error) {
	return f.readable, nil
}

// TestVerifyIssuesReadableDetectsDroppedWrite is the RED/GREEN detector test
// for hq-dzmd0: bd create must not report success unless the bead it claims
// to have created is subsequently readable. It simulates the failure mode
// directly — CreateIssue returned no error, but the issue is not readable
// back — rather than exercising the normal create path, which passes both
// before and after this fix and certifies nothing.
func TestVerifyIssuesReadableDetectsDroppedWrite(t *testing.T) {
	fake := &fakeDroppedWriteStore{readable: nil} // dropped write: nothing comes back
	err := verifyIssuesReadable(context.Background(), fake, []string{"bd-dropped"})
	if err == nil {
		t.Fatal("verifyIssuesReadable: expected an error for a write reported successful but not readable back from storage, got nil")
	}
	if !strings.Contains(err.Error(), "bd-dropped") {
		t.Fatalf("verifyIssuesReadable: error should name the missing issue id, got: %v", err)
	}
}

func TestVerifyIssuesReadableSucceedsWhenReadable(t *testing.T) {
	fake := &fakeDroppedWriteStore{readable: []*types.Issue{{ID: "bd-ok"}}}
	if err := verifyIssuesReadable(context.Background(), fake, []string{"bd-ok"}); err != nil {
		t.Fatalf("verifyIssuesReadable: unexpected error for a readable issue: %v", err)
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
