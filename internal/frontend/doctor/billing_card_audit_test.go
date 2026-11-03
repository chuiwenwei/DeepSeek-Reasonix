package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/base/testenv"
	"reasonix/internal/contract/config"
	"reasonix/internal/contract/pricing"
)

const billingAuditDay = "2026-09-10"

// The exact report `reasonix doctor billing` printed for a config with no
// providers before stored rows were audited. The audit is additive: everything
// here must still render byte-for-byte.
const billingOutputBeforeCardAudit = "" +
	"reasonix doctor billing\n" +
	"  display preference: auto\n" +
	"  display resolved:   \n" +
	"  fx source:          disabled\n" +
	"  fx enabled:         false\n" +
	"  auto strategy:      wallet currency, then pricing basis\n" +
	"\n" +
	"  providers:\n" +
	"\n" +
	"  notes:\n" +
	"    - User-custom prices always win over the official catalog.\n" +
	"    - MiMo Token Plan costs are pay-as-you-go equivalents, not plan invoices.\n" +
	"    - Runtime FX is disabled; only identity and official regional rate-card estimates are used.\n" +
	"    - Switching display_currency never rewrites provider billing_currency or list prices.\n"

func billingAuditRange(t *testing.T) (time.Time, time.Time) {
	t.Helper()
	from, err := time.Parse("2006-01-02", billingAuditDay)
	if err != nil {
		t.Fatalf("parse day: %v", err)
	}
	return from, from.Add(24*time.Hour - time.Second)
}

func writeBillingAuditRows(t *testing.T, dir string, rows ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := strings.Join(rows, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, billingAuditDay+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("write rows: %v", err)
	}
}

func billingAuditRow(card pricing.RateCard) string {
	return `{"ts":"2026-09-10T10:00:00Z","model":"deepseek/deepseek-flash","source":"cli","total":100,` +
		`"cost_currency":"` + card.Currency + `","pricing_fingerprint":"` + pricing.PricingFingerprint(card) + `"}`
}

func collectBillingAudit(t *testing.T, dir string) BillingReport {
	t.Helper()
	from, to := billingAuditRange(t)
	return CollectBillingWithOptions(&config.Config{}, BillingOptions{From: from, To: to, StatsDir: dir})
}

func jsonOf(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func wantCount(t *testing.T, obj map[string]any, key string, want float64) {
	t.Helper()
	got, ok := obj[key]
	if !ok {
		t.Fatalf("JSON has no %q: %v", key, obj)
	}
	if got != want {
		t.Fatalf("JSON %q = %v, want %v", key, got, want)
	}
}

// One resolvable card, one row that names nothing, and one fingerprint the
// published history cannot resolve must be three different answers. The old
// report could not distinguish them because it did not read rows at all.
func TestBillingCardAuditSeparatesTheThreeRowKinds(t *testing.T) {
	dir := testenv.TempDir(t)
	resolvable := pricing.RateCard{CacheHit: 0.003, Input: 0.15, Output: 0.6, Currency: "USD"}
	// The peak half of a superseded DeepSeek generation: shipped, priced real
	// turns, but not enumerated by the published history.
	supersededPeak := pricing.RateCard{CacheHit: 0.014, Input: 0.44, Output: 1.32, Currency: "USD"}
	writeBillingAuditRows(t, dir,
		billingAuditRow(resolvable),
		`{"ts":"2026-09-10T11:00:00Z","model":"deepseek/deepseek-flash","source":"cli","total":100,"cost_currency":"USD"}`,
		billingAuditRow(supersededPeak),
	)
	rep := collectBillingAudit(t, dir)
	audit := rep.CardAudit
	if audit == nil || !audit.Known {
		t.Fatalf("card audit = %+v, want a known range", audit)
	}
	if audit.Rows != 3 || *audit.Named != 2 || *audit.Unresolvable != 1 || *audit.Unrecorded != 1 {
		t.Fatalf("audit = rows %d named %d unresolvable %d unrecorded %d, want 3/2/1/1",
			audit.Rows, *audit.Named, *audit.Unresolvable, *audit.Unrecorded)
	}

	// The provider report is the exact text it printed before this change; the
	// audit is appended, not folded into it.
	text := RenderBillingText(rep)
	if !strings.HasPrefix(text, billingOutputBeforeCardAudit) {
		t.Fatalf("provider report changed.\nwant prefix:\n%s\ngot:\n%s", billingOutputBeforeCardAudit, text)
	}
	for _, want := range []string{
		"  stored rows:\n",
		"    rows: 3\n",
		"    named a card: 2\n",
		"    unresolvable: 1\n",
		"    no card recorded: 1\n",
		"    note: every unresolvable card names a superseded peak card",
		"not corruption",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text missing %q:\n%s", want, text)
		}
	}

	obj := jsonOf(t, audit)
	wantCount(t, obj, "rows", 3)
	wantCount(t, obj, "named", 2)
	wantCount(t, obj, "unresolvable", 1)
	wantCount(t, obj, "unrecorded", 1)
	wantCount(t, obj, "superseded_peak", 1)
	if known, _ := obj["known"].(bool); !known {
		t.Fatalf("JSON known = %v, want true", obj["known"])
	}
}

// Fully named, fully resolvable rows are measured zeros, not unknowns.
func TestBillingCardAuditFullyResolvedRangeMeasuresZeros(t *testing.T) {
	dir := testenv.TempDir(t)
	card := pricing.RateCard{CacheHit: 0.003, Input: 0.15, Output: 0.6, Currency: "USD"}
	writeBillingAuditRows(t, dir,
		billingAuditRow(card),
		billingAuditRow(card),
		billingAuditRow(card),
	)
	rep := collectBillingAudit(t, dir)
	audit := rep.CardAudit
	if audit == nil || !audit.Known {
		t.Fatalf("card audit = %+v, want a known range", audit)
	}
	if audit.Rows != 3 || *audit.Named != 3 || *audit.Unresolvable != 0 || *audit.Unrecorded != 0 {
		t.Fatalf("audit = rows %d named %d unresolvable %d unrecorded %d, want 3/3/0/0",
			audit.Rows, *audit.Named, *audit.Unresolvable, *audit.Unrecorded)
	}
	if audit.Note != "" {
		t.Fatalf("a fully resolved range carried a note: %q", audit.Note)
	}
	text := RenderBillingText(rep)
	for _, want := range []string{"    unresolvable: 0\n", "    no card recorded: 0\n"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text missing a measured zero %q:\n%s", want, text)
		}
	}
	obj := jsonOf(t, audit)
	wantCount(t, obj, "unresolvable", 0)
	wantCount(t, obj, "unrecorded", 0)
}

// A row written before pricing_fingerprint existed names nothing. That is
// unknown, never unresolvable: the card did not fail to resolve, none was named.
func TestBillingCardAuditOldRowIsUnrecordedNotUnresolvable(t *testing.T) {
	dir := testenv.TempDir(t)
	writeBillingAuditRows(t, dir,
		`{"ts":"2026-09-10T10:00:00Z","model":"deepseek/deepseek-flash","source":"cli","total":100,"cost_currency":"USD"}`,
	)
	rep := collectBillingAudit(t, dir)
	audit := rep.CardAudit
	if audit == nil || !audit.Known {
		t.Fatalf("card audit = %+v, want a known range", audit)
	}
	if *audit.Named != 0 || *audit.Unresolvable != 0 || *audit.Unrecorded != 1 {
		t.Fatalf("audit = named %d unresolvable %d unrecorded %d, want 0/0/1",
			*audit.Named, *audit.Unresolvable, *audit.Unrecorded)
	}
}

// Records the report cannot read are unknown. Neither count may print 0, because
// 0 claims the range was read and found clean.
func TestBillingCardAuditUnreadableRowsAreUnknownNotZero(t *testing.T) {
	dir := testenv.TempDir(t)
	file := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	rep := collectBillingAudit(t, file)
	audit := rep.CardAudit
	if audit == nil {
		t.Fatal("no card audit")
	}
	if audit.Known {
		t.Fatal("an unreadable stats root reported known")
	}
	if audit.Named != nil || audit.Unresolvable != nil || audit.Unrecorded != nil {
		t.Fatalf("unreadable range carried counts: %+v", audit)
	}
	text := RenderBillingText(rep)
	if !strings.Contains(text, "unknown (the stored rows could not be read)") {
		t.Fatalf("text does not say unknown:\n%s", text)
	}
	if strings.Contains(text, "unresolvable: 0") || strings.Contains(text, "no card recorded: 0") {
		t.Fatalf("unreadable range rendered a measured zero:\n%s", text)
	}
	obj := jsonOf(t, audit)
	if known, _ := obj["known"].(bool); known {
		t.Fatalf("JSON known = %v, want false", obj["known"])
	}
	for _, key := range []string{"named", "unresolvable", "unrecorded"} {
		if _, ok := obj[key]; ok {
			t.Fatalf("unreadable range serialized %q: %v", key, obj)
		}
	}
}
