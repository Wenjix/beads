package versioncontrolops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// FilterConn supports branch filtering and transactional blocked-state repair.
type FilterConn interface {
	BlockedRecomputeConn
}

// FederationWarnWriter receives the one-time warning for excluded issue history.
var FederationWarnWriter io.Writer = os.Stderr

var excludedHistoryWarningOnce sync.Once

// StageFilteredBranch creates and filters a staging branch, then restores the source branch.
func StageFilteredBranch(ctx context.Context, conn FilterConn, sourceBranch, stagingBranch string, excludeTypes []string) (int64, error) {
	if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH(?, ?)", stagingBranch, sourceBranch); err != nil {
		return 0, fmt.Errorf("federation filter: create staging branch: %w", err)
	}
	if err := CheckoutBranch(ctx, conn, stagingBranch); err != nil {
		return 0, fmt.Errorf("federation filter: checkout staging: %w", err)
	}
	var removed int64
	for _, excludeType := range excludeTypes {
		query := "DELETE FROM issues WHERE issue_type = ?"
		args := []any{excludeType}
		if excludeType == "wisp" {
			query, args = "DELETE FROM issues WHERE ephemeral = 1", nil
		}
		result, err := conn.ExecContext(ctx, query, args...)
		if err == nil {
			if n, _ := result.RowsAffected(); n > 0 {
				removed += n
			}
		}
	}
	if removed > 0 {
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return removed, fmt.Errorf("federation filter: begin staged is_blocked recompute: %w", err)
		}
		if _, err := issueops.RecomputeAllIsBlockedInTx(ctx, tx); err != nil {
			_ = tx.Rollback()
			return removed, fmt.Errorf("federation filter: recompute staged is_blocked: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return removed, fmt.Errorf("federation filter: commit staged is_blocked recompute: %w", err)
		}
		if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?)", "federation: exclude private issue types"); err != nil {
			return removed, fmt.Errorf("federation filter: commit filtered state: %w", err)
		}
	}
	if err := CheckoutBranch(ctx, conn, sourceBranch); err != nil {
		return removed, fmt.Errorf("federation filter: restore branch %s: %w", sourceBranch, err)
	}
	return removed, nil
}

// KeepFilteredPushLocal makes sourceBranch descend from the pushed filtered commit without taking its deletions, so merging the peer branch back cannot delete local excluded issues.
func KeepFilteredPushLocal(ctx context.Context, conn FilterConn, sourceBranch, stagingBranch string) error {
	restoreSource := func(cause error) error {
		if err := CheckoutBranch(context.WithoutCancel(ctx), conn, sourceBranch); err != nil {
			cause = errors.Join(cause, fmt.Errorf("restore branch %s: %w", sourceBranch, err))
		}
		return fmt.Errorf("federation filter: keep excluded issues local: %w", cause)
	}
	abortAndRestore := func(cause error) error {
		cleanupCtx := context.WithoutCancel(ctx)
		if err := schema.DrainCall(cleanupCtx, conn, "CALL DOLT_MERGE('--abort')"); err != nil {
			cause = errors.Join(cause, fmt.Errorf("abort merge: %w", err))
		}
		if err := CheckoutBranch(cleanupCtx, conn, sourceBranch); err != nil {
			cause = errors.Join(cause, fmt.Errorf("restore branch %s: %w", sourceBranch, err))
		}
		return fmt.Errorf("federation filter: keep excluded issues local: %w", cause)
	}

	if err := CheckoutBranch(ctx, conn, stagingBranch); err != nil {
		return restoreSource(fmt.Errorf("checkout staging branch %s: %w", stagingBranch, err))
	}
	if err := schema.DrainCall(ctx, conn, "CALL DOLT_REVERT('HEAD')"); err != nil {
		return restoreSource(fmt.Errorf("revert filtered staging commit: %w", err))
	}
	if err := CheckoutBranch(ctx, conn, sourceBranch); err != nil {
		return restoreSource(fmt.Errorf("checkout source branch %s: %w", sourceBranch, err))
	}

	mergeRow, err := schema.CallReturningRow(ctx, conn, "CALL DOLT_MERGE(?, '-m', ?)",
		stagingBranch, "federation: keep excluded issues after filtered push")
	if err != nil {
		return abortAndRestore(fmt.Errorf("merge reverted staging branch %s: %w", stagingBranch, err))
	}
	conflicts, ok := mergeRow.Int("conflicts")
	if !ok {
		return abortAndRestore(fmt.Errorf("merge reverted staging branch %s returned no conflict status", stagingBranch))
	}
	if conflicts != 0 {
		return abortAndRestore(fmt.Errorf("merge reverted staging branch %s reported conflicts", stagingBranch))
	}
	return nil
}

// WarnExcludedHistory warns once when the push tip was filtered but history remains readable.
func WarnExcludedHistory(removed int64) {
	if removed <= 0 {
		return
	}
	excludedHistoryWarningOnce.Do(func() {
		_, _ = fmt.Fprintf(FederationWarnWriter, "Warning: federation.exclude_types removed %d issue(s) from this push, but earlier commits in the pushed history still contain them; a peer that reads history can recover them. See https://beads.gascity.com/multi-agent/federation\n", removed)
	})
}
