package stats

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/base/testenv"
	"reasonix/internal/contract/pricing"
)

const cardAuditDay = "2026-09-10"

func cardAuditRange(t *testing.T) (time.Time, time.Time) {
	t.Helper()
	from, err := time.Parse(dayLayout, cardAuditDay)
	if err != nil {
		t.Fatalf("parse day: %v", err)
	}
	return from, from.Add(24*time.Hour - time.Second)
}

// writeCardAuditRows writes one daily file with the given JSON rows.
func writeCardAuditRows(t *testing.T, dir string, rows ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := strings.Join(rows, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, cardAuditDay+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("write rows: %v", err)
	}
}

func cardAuditRow(card pricing.RateCard) string {
	return `{"ts":"2026-09-10T10:00:00Z","model":"deepseek/deepseek-flash","source":"cli","total":100,` +
		`"cost_currency":"` + card.Currency + `","pricing_fingerprint":"` + pricing.PricingFingerprint(card) + `"}`
}

// The audit has to keep three states apart: a row that names a card the history
// resolves, a row that names one it does not, and a row that names nothing.
func TestAuditCardsSeparatesUnrecordedFromUnresolvable(t *testing.T) {
	dir := testenv.TempDir(t)
	resolvable := pricing.RateCard{CacheHit: 0.003, Input: 0.15, Output: 0.6, Currency: "USD"}
	supersededPeak := pricing.RateCard{CacheHit: 0.014, Input: 0.44, Output: 1.32, Currency: "USD"}
	writeCardAuditRows(t, dir,
		cardAuditRow(resolvable),
		`{"ts":"2026-09-10T11:00:00Z","model":"deepseek/deepseek-flash","source":"cli","total":100,"cost_currency":"USD"}`,
		cardAuditRow(supersededPeak),
		`{"ts":"2026-09-10T12:00:00Z","turn":true}`,
	)
	from, to := cardAuditRange(t)
	audit, err := AuditCards(dir, from, to)
	if err != nil {
		t.Fatalf("AuditCards: %v", err)
	}
	if !audit.Known {
		t.Fatal("a readable range reported unknown")
	}
	if audit.Rows != 3 {
		t.Fatalf("rows = %d, want 3 (a turn marker is not a usage row)", audit.Rows)
	}
	if audit.Named != 2 {
		t.Fatalf("named = %d, want 2", audit.Named)
	}
	if audit.Unrecorded != 1 {
		t.Fatalf("unrecorded = %d, want 1", audit.Unrecorded)
	}
	if audit.Unresolvable != 1 {
		t.Fatalf("unresolvable = %d, want 1", audit.Unresolvable)
	}
	if audit.SupersededPeak != 1 {
		t.Fatalf("supersededPeak = %d, want 1", audit.SupersededPeak)
	}
	if !strings.Contains(audit.Note, "known gap") {
		t.Fatalf("note does not name the known gap: %q", audit.Note)
	}
}

// A fingerprint no shipped card matches is a real gap, not the known one.
func TestAuditCardsDoesNotCallAnUnknownCardTheSupersededPeakGap(t *testing.T) {
	dir := testenv.TempDir(t)
	writeCardAuditRows(t, dir,
		`{"ts":"2026-09-10T10:00:00Z","model":"deepseek/deepseek-flash","source":"cli","total":100,"cost_currency":"USD","pricing_fingerprint":"0000000000000000"}`,
	)
	from, to := cardAuditRange(t)
	audit, err := AuditCards(dir, from, to)
	if err != nil {
		t.Fatalf("AuditCards: %v", err)
	}
	if audit.Unresolvable != 1 || audit.SupersededPeak != 0 {
		t.Fatalf("unresolvable=%d supersededPeak=%d, want 1 and 0", audit.Unresolvable, audit.SupersededPeak)
	}
	if !strings.Contains(audit.Note, "none of them matches a card this project shipped") {
		t.Fatalf("note = %q", audit.Note)
	}
}

// A range with no rows measured zero, which is not the same as unknown.
func TestAuditCardsEmptyRangeIsMeasuredZero(t *testing.T) {
	dir := testenv.TempDir(t)
	from, to := cardAuditRange(t)
	audit, err := AuditCards(dir, from, to)
	if err != nil {
		t.Fatalf("AuditCards: %v", err)
	}
	if !audit.Known || audit.Rows != 0 || audit.Named != 0 || audit.Unresolvable != 0 || audit.Unrecorded != 0 {
		t.Fatalf("empty range = %+v, want known measured zeros", audit)
	}
}

// An unreadable root must report unknown, never zeros: a zero is a measurement.
func TestAuditCardsUnreadableRootIsUnknown(t *testing.T) {
	dir := testenv.TempDir(t)
	file := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	from, to := cardAuditRange(t)
	audit, err := AuditCards(file, from, to)
	if err == nil {
		t.Fatal("AuditCards read a regular file as a stats directory")
	}
	if audit.Known {
		t.Fatal("an unreadable root reported known")
	}
}
