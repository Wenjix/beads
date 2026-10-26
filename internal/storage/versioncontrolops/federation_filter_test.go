package versioncontrolops

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestWarnExcludedHistory(t *testing.T) {
	previous := FederationWarnWriter
	var output bytes.Buffer
	FederationWarnWriter = &output
	t.Cleanup(func() { FederationWarnWriter = previous })

	WarnExcludedHistory(0)
	WarnExcludedHistory(2)
	WarnExcludedHistory(5)
	want := "Warning: federation.exclude_types removed 2 issue(s) from this push, but earlier commits in the pushed history still contain them; a peer that reads history can recover them. See https://beads.gascity.com/multi-agent/federation\n"
	if output.String() != want {
		t.Fatalf("warning = %q, want %q", output.String(), want)
	}
}

func TestKeepFilteredPushLocalRevertsAndMerges(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT(?)")).
		WithArgs("staging").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_REVERT('HEAD')")).
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT(?)")).
		WithArgs("main").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_MERGE(?, '-m', ?)")).
		WithArgs("staging", "federation: keep excluded issues after filtered push").
		WillReturnRows(sqlmock.NewRows([]string{"conflicts", "message"}).AddRow(0, "merge successful"))

	if err := KeepFilteredPushLocal(context.Background(), db, "main", "staging"); err != nil {
		t.Fatalf("KeepFilteredPushLocal: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}
}

func TestKeepFilteredPushLocalAbortsMergeConflict(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT(?)")).
		WithArgs("staging").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_REVERT('HEAD')")).
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT(?)")).
		WithArgs("main").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_MERGE(?, '-m', ?)")).
		WithArgs("staging", "federation: keep excluded issues after filtered push").
		WillReturnRows(sqlmock.NewRows([]string{"conflicts", "message"}).AddRow(1, "conflicts"))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_MERGE('--abort')")).
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT(?)")).
		WithArgs("main").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err = KeepFilteredPushLocal(context.Background(), db, "main", "staging")
	if err == nil || !strings.HasPrefix(err.Error(), "federation filter: ") ||
		!strings.Contains(err.Error(), "reported conflicts") {
		t.Fatalf("KeepFilteredPushLocal error = %v, want wrapped merge conflict error", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sqlmock expectations: %v", err)
	}
}
