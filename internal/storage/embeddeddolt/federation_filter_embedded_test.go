//go:build cgo

package embeddeddolt_test

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
)

func TestFilteredFederationSyncExcludesWispsAndWarnsOnce(t *testing.T) {
	ctx := t.Context()
	te := newTestEnv(t, "filter")
	peer := &storage.FederationPeer{
		Name: "filter-peer", RemoteURL: "file://" + filepath.Join(t.TempDir(), "remote"),
	}
	if err := te.store.AddFederationPeer(ctx, peer); err != nil {
		t.Fatalf("AddFederationPeer: %v", err)
	}
	excluded := &types.Issue{
		ID: "filter-excluded", Title: "excluded", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
	}
	kept := &types.Issue{
		ID: "filter-kept", Title: "kept", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask,
	}
	for _, issue := range []*types.Issue{excluded, kept} {
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
		}
	}
	if err := te.store.PushTo(ctx, peer.Name); err != nil {
		t.Fatalf("initial PushTo: %v", err)
	}

	previousExcludeTypes := config.GetFederationConfig().ExcludeTypes
	config.Set("federation.exclude_types", []string{"wisp"})
	t.Cleanup(func() { config.Set("federation.exclude_types", previousExcludeTypes) })
	previousWriter := versioncontrolops.FederationWarnWriter
	var warnings bytes.Buffer
	versioncontrolops.FederationWarnWriter = &warnings
	t.Cleanup(func() { versioncontrolops.FederationWarnWriter = previousWriter })

	result, err := te.store.Sync(ctx, peer.Name, "")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !result.Pushed || result.PushError != nil {
		t.Fatalf("Sync push result: pushed=%t push error=%v", result.Pushed, result.PushError)
	}
	versioncontrolops.WarnExcludedHistory(1)

	var excludedCount, keptCount int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM issues AS OF 'remotes/filter-peer/main' WHERE id = ?",
		[]any{excluded.ID}, &excludedCount)
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM issues AS OF 'remotes/filter-peer/main' WHERE id = ?",
		[]any{kept.ID}, &keptCount)
	if excludedCount != 0 || keptCount != 1 {
		t.Fatalf("pushed tip counts: excluded=%d kept=%d, want 0 and 1", excludedCount, keptCount)
	}
	wantWarning := "Warning: federation.exclude_types removed 1 issue(s) from this push, but earlier commits in the pushed history still contain them; a peer that reads history can recover them. See https://beads.gascity.com/multi-agent/federation\n"
	if got := warnings.String(); got != wantWarning {
		t.Fatalf("warning = %q, want %q", got, wantWarning)
	}
}
