package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"reasonix/internal/base/testenv"
	"reasonix/internal/contract/config"
	"reasonix/internal/contract/pricing"
)

// The JSON report is what a script gates on, so the CLI has to carry the three
// counts through, not only the human text.
func TestDoctorBillingJSONCarriesCardAuditCounts(t *testing.T) {
	home := testenv.TempDir(t)
	t.Setenv("REASONIX_HOME", home)
	statsDir := config.StatsDir()
	if statsDir == "" {
		t.Fatal("no stats dir under the isolated home")
	}
	if err := os.MkdirAll(statsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	card := pricing.RateCard{CacheHit: 0.003, Input: 0.15, Output: 0.6, Currency: "USD"}
	now := time.Now()
	body := `{"ts":"` + now.UTC().Format(time.RFC3339) + `","model":"deepseek/deepseek-flash","source":"cli","total":100,"cost_currency":"USD","pricing_fingerprint":"` + pricing.PricingFingerprint(card) + `"}` + "\n" +
		`{"ts":"` + now.UTC().Format(time.RFC3339) + `","model":"deepseek/deepseek-flash","source":"cli","total":100,"cost_currency":"USD"}` + "\n"
	if err := os.WriteFile(filepath.Join(statsDir, now.Format("2006-01-02")+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if rc := doctorBillingCommand([]string{"--json", "--days", "1"}); rc != 0 {
			t.Fatalf("doctor billing rc = %d, want 0", rc)
		}
	})
	var decoded struct {
		CardAudit struct {
			Known        bool `json:"known"`
			Rows         int  `json:"rows"`
			Named        *int `json:"named"`
			Unresolvable *int `json:"unresolvable"`
			Unrecorded   *int `json:"unrecorded"`
		} `json:"card_audit"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("doctor billing --json is not JSON: %v\n%s", err, out)
	}
	a := decoded.CardAudit
	if !a.Known || a.Rows != 2 || a.Named == nil || a.Unresolvable == nil || a.Unrecorded == nil {
		t.Fatalf("card_audit = %+v, want known rows=2 with every count", a)
	}
	if *a.Named != 1 || *a.Unresolvable != 0 || *a.Unrecorded != 1 {
		t.Fatalf("counts = named %d unresolvable %d unrecorded %d, want 1/0/1",
			*a.Named, *a.Unresolvable, *a.Unrecorded)
	}
}

func TestDoctorBillingRejectsAnUnusableRange(t *testing.T) {
	for _, args := range [][]string{
		{"--days", "0"},
		{"--days", "366"},
		{"--from", "2026-09-01"},
		{"--to", "2026-09-01"},
		{"--from", "2026-09-02", "--to", "2026-09-01"},
		{"--from", "2026-09-01", "--to", "2026-09-02", "--days", "7"},
		{"--from", "nope", "--to", "2026-09-02"},
	} {
		if rc := doctorBillingCommand(args); rc != 2 {
			t.Fatalf("doctor billing %v rc = %d, want usage error 2", args, rc)
		}
	}
}
