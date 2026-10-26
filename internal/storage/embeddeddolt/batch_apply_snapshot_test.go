//go:build cgo

package embeddeddolt_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/batchfixtures"
	"github.com/steveyegge/beads/internal/storage/sqlcount"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
)

// TestBatchApplyHydratesEachIssueOnce pins that a batch touching one large
// issue many times retains one snapshot of it, not one per item: memory stays
// at the issue's size instead of growing with the item count.
func TestBatchApplyHydratesEachIssueOnce(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	fixture := newPristineEmbeddedDoltFixture(t, "hydrateonce")
	t.Cleanup(func() { closeEmbeddedDoltStore(t, fixture.store) })

	const updates = 40
	blob, err := json.Marshal(map[string]string{"blob": strings.Repeat("x", 64<<10)})
	if err != nil {
		t.Fatal(err)
	}
	items := []issueops.ApplyItem{{
		Kind: issueops.ItemCreate,
		Create: &issueops.CreateItem{Key: "big", Issue: &types.Issue{
			Title: "big", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Metadata: blob,
		}},
	}}
	for i := 1; i <= updates; i++ {
		items = append(items, issueops.ApplyItem{
			Kind: issueops.ItemUpdate,
			Update: &issueops.UpdateItem{
				Target: issueops.Ref{Key: "big"},
				Patch:  issueops.IssuePatch{Title: issueops.Field[string]{Set: true, Value: fmt.Sprintf("big %d", i)}},
			},
		})
	}

	db, cleanup, err := openCountedConn(ctx, fixture.dataDir, fixture.database, &sqlcount.Counts{})
	if err != nil {
		t.Fatalf("openCountedConn: %v", err)
	}
	defer cleanup()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	result, _, err := storageissueops.ApplyBatchInTx(ctx, tx, batchfixtures.UncappedPlan(issueops.ApplyBatchRequest{Actor: "tester", Items: items}))
	if err != nil {
		t.Fatalf("ApplyBatchInTx: %v", err)
	}

	snapshots := map[*types.Issue]bool{}
	versions := map[int64]bool{}
	for _, item := range result.Items {
		if item.Issue == nil {
			t.Fatalf("item %+v carries no snapshot", item)
		}
		snapshots[item.Issue] = true
		versions[item.RowVersion] = true
	}
	if len(snapshots) != 1 {
		t.Fatalf("%d items retained %d snapshots of one issue, want 1", len(result.Items), len(snapshots))
	}
	final := result.Items[len(result.Items)-1]
	if final.Issue.Title != fmt.Sprintf("big %d", updates) {
		t.Errorf("snapshot title = %q, want the final state", final.Issue.Title)
	}
	if final.Issue.RowVersion != final.RowVersion {
		t.Errorf("snapshot version = %d, want the last item's %d", final.Issue.RowVersion, final.RowVersion)
	}
	if len(final.Issue.Metadata) < 64<<10 {
		t.Errorf("snapshot metadata is %d bytes, want the created blob", len(final.Issue.Metadata))
	}
	if len(versions) < 2 {
		t.Errorf("items report %d distinct versions, want each item's own token after it", len(versions))
	}
}
