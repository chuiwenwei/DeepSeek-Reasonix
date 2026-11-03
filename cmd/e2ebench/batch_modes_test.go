package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The two modes this change adds are SWE-bench (units are instances) and diff
// (the unit is one PR, the attempts are retries). Suite mode stays the control
// below: its manifest and audit output must not move.

func swebenchFixtureRows() []result {
	return []result{
		{
			task:       task{ID: "astropy__astropy-12907"},
			runMetrics: runMetrics{PromptTokens: 1000, CompletionTokens: 100, CacheHitTokens: 400, CacheMissTokens: 600, Cost: 0.02, Currency: "USD", Outcome: "solved"},
			Passed:     true, WallMs: 900,
		},
		{
			task:       task{ID: "django__django-11099"},
			runMetrics: runMetrics{PromptTokens: 2000, CompletionTokens: 200, Cost: 0.04, Currency: "USD", Outcome: "wrong_patch"},
			Passed:     false, WallMs: 700,
		},
		{
			task:       task{ID: "sympy__sympy-20590"},
			runMetrics: runMetrics{Outcome: "timeout"},
			Passed:     false, Unaccounted: true,
		},
	}
}

func swebenchFixtureProvenance(dir string) batchProvenance {
	ids := []string{"astropy__astropy-12907", "django__django-11099", "sympy__sympy-20590"}
	count, digest := idSetDigest(ids)
	return batchProvenance{
		dir: dir, mode: "swebench", started: timeAt("2026-11-03T00:00:00Z"), planned: count,
		axes:   []batchFlag{{"permission", "auto"}, {"dataset", "princeton-nlp/SWE-bench_Verified"}},
		tasks:  batchTaskSet{Count: count, Digest: digest},
		commit: "0123456789abcdef0123456789abcdef01234567", dirty: new(false),
		model: "deepseek-flash", provider: "deepseek",
	}
}

func diffFixtureRows() []diffBatchRow {
	return []diffBatchRow{
		{
			Attempt: 1, Passed: false, TestsPass: true, NewTests: 2, Pins: 1, AssertionPins: 1,
			AddedTestLines: 40, Covered: 10, CoverTotal: 20, MutationCaught: 1, MutationTotal: 2,
			BuildOK: true, PromptTokens: 1200, CompletionTokens: 80,
			CacheHitTokens: 500, CacheMissTokens: 700, Cost: 0.03, Currency: "USD",
		},
		{
			Attempt: 2, Selected: true, Passed: true, TestsPass: true, NewTests: 3, Pins: 2, AssertionPins: 2,
			AddedTestLines: 55, Covered: 15, CoverTotal: 20, MutationCaught: 2, MutationTotal: 2,
			BuildOK: true, PromptTokens: 900, CompletionTokens: 60,
			CacheHitTokens: 400, CacheMissTokens: 500, Cost: 0.02, Currency: "USD",
		},
	}
}

func diffFixtureProvenance(dir string) batchProvenance {
	ids := []string{"cmd/e2ebench/batch.go", "cmd/e2ebench/audit.go"}
	count, digest := idSetDigest(ids)
	return batchProvenance{
		dir: dir, mode: "diff", started: timeAt("2026-11-03T00:00:00Z"), planned: len(diffFixtureRows()),
		axes:   []batchFlag{{"base", "origin/studio"}, {"attempts", "2"}},
		tasks:  batchTaskSet{Count: count, Digest: digest},
		commit: "0123456789abcdef0123456789abcdef01234567", dirty: new(false),
		model: "deepseek-flash", provider: "deepseek",
	}
}

// writeModeFixture writes one synthetic batch for a newly covered mode and
// returns its directory and manifest.
func writeModeFixture(t *testing.T, mode string) (string, batchManifest) {
	t.Helper()
	dir := t.TempDir()
	switch mode {
	case "swebench":
		if err := writeSwebenchBatch(swebenchFixtureProvenance(dir), swebenchFixtureRows()); err != nil {
			t.Fatalf("write swebench batch: %v", err)
		}
	case "diff":
		if err := writeDiffBatch(diffFixtureProvenance(dir), diffFixtureRows()); err != nil {
			t.Fatalf("write diff batch: %v", err)
		}
	default:
		t.Fatalf("no fixture for mode %q", mode)
	}
	return dir, readManifest(t, dir)
}

// Every mode that produces a quotable result set writes the same manifest field
// groups. A mode cannot silently fall back to the suite's denominator either.
func TestEveryNewlyCoveredModeWritesManifestV1(t *testing.T) {
	want := map[string]struct{ unit, definition string }{
		"swebench": {swebenchDenominatorUnit, swebenchDenominatorDefinition},
		"diff":     {diffDenominatorUnit, diffDenominatorDefinition},
	}
	for _, mode := range []string{"swebench", "diff"} {
		t.Run(mode, func(t *testing.T) {
			dir, m := writeModeFixture(t, mode)
			if m.SchemaVersion != batchSchemaVersion || strings.TrimSpace(m.Scope) == "" {
				t.Fatalf("schema = %d scope = %q, want v%d with a named claim", m.SchemaVersion, m.Scope, batchSchemaVersion)
			}
			if m.Over.Command.Mode != mode {
				t.Fatalf("command mode = %q, want %q", m.Over.Command.Mode, mode)
			}
			if len(m.Over.Command.Axes) == 0 {
				t.Fatalf("mode %q records no axes: what the figures are over is unstated", mode)
			}
			if m.Over.Tasks.Count == 0 || strings.TrimSpace(m.Over.Tasks.Digest) == "" {
				t.Fatalf("task set = %+v, want a non-empty digest", m.Over.Tasks)
			}
			if m.Over.Denominator.Unit != want[mode].unit || m.Over.Denominator.Definition != want[mode].definition {
				t.Fatalf("denominator = %+v, want %q / %q", m.Over.Denominator, want[mode].unit, want[mode].definition)
			}
			if m.Over.Denominator.Definition == denominatorDefinition {
				t.Fatalf("mode %q reused the suite's task denominator", mode)
			}
			if m.Over.Denominator.Value < 1 {
				t.Fatalf("denominator value = %d, want at least one graded unit", m.Over.Denominator.Value)
			}
			if m.Producer.Commit == "" || m.Producer.StartedAt == "" || m.Producer.FinishedAt == "" ||
				m.Producer.HarnessVersion == "" || m.Producer.GoVersion == "" {
				t.Fatalf("producer = %+v, want a committed, time-stamped producer", m.Producer)
			}
			if m.Trust.ResultsSHA256 == "" || m.Trust.Retention == "" {
				t.Fatalf("trust = %+v, want a row digest and a retention line", m.Trust)
			}
			ledger := m.Trust.Ledger
			for name, count := range map[string]*int{
				"planned": ledger.Planned, "attempted": ledger.Attempted, "completed": ledger.Completed,
				"scored": ledger.Scored, "failed": ledger.Failed, "unmeasured": ledger.Unmeasured,
				"excluded": ledger.Excluded,
			} {
				if count == nil {
					t.Fatalf("ledger.%s is absent; the seven counts are required in every mode", name)
				}
			}
			for _, name := range []string{"planned", "scored", "failed", "excluded"} {
				if strings.TrimSpace(ledger.Definitions[name]) == "" {
					t.Fatalf("ledger.definitions.%s is empty: the count cannot be reinterpreted after the fact", name)
				}
			}
			if len(ledger.Definitions) != len(ledgerDefinitions) {
				t.Fatalf("ledger defines %d counts, want %d", len(ledger.Definitions), len(ledgerDefinitions))
			}
			if out, err := auditBatch(dir); err != nil {
				t.Fatalf("a producer-written %s batch did not audit: %v\n%s", mode, err, out)
			}
		})
	}
}

// tamperRows rewrites results.jsonl without touching the manifest, which is
// what a changed number looks like.
func tamperResultRows(t *testing.T, dir string, rows []result) {
	t.Helper()
	var b strings.Builder
	for _, r := range rows {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("encode row: %v", err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, batchResultsName), []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write rows: %v", err)
	}
}

func tamperDiffRows(t *testing.T, dir string, rows []diffBatchRow) {
	t.Helper()
	var b strings.Builder
	for _, r := range rows {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("encode row: %v", err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, batchResultsName), []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write rows: %v", err)
	}
}

// A verified batch names the figure that moved rather than only failing the
// digest: the reader has to see which number no longer follows from the rows.
func TestAuditNamesTheFigureThatMovedInEveryMode(t *testing.T) {
	t.Run("swebench", func(t *testing.T) {
		dir, _ := writeModeFixture(t, "swebench")
		rows := swebenchFixtureRows()
		rows[0].Passed = false // the manifest claims one solve; the rows now give zero
		tamperResultRows(t, dir, rows)
		out, err := auditBatch(dir)
		if err == nil {
			t.Fatalf("a batch whose solve was removed audited clean:\n%s", out)
		}
		if !strings.Contains(out, "solved: manifest 1, rows 0") {
			t.Fatalf("audit output does not name the moved solve:\n%s", out)
		}
	})

	t.Run("diff", func(t *testing.T) {
		dir, _ := writeModeFixture(t, "diff")
		rows := diffFixtureRows()
		rows[1].PromptTokens += 50 // the selected attempt's spend moved
		tamperDiffRows(t, dir, rows)
		out, err := auditBatch(dir)
		if err == nil {
			t.Fatalf("a batch whose token total moved audited clean:\n%s", out)
		}
		if !strings.Contains(out, "prompt_tokens: manifest 2100, rows 2150") {
			t.Fatalf("audit output does not name the moved token total:\n%s", out)
		}
	})

	t.Run("diff selection", func(t *testing.T) {
		dir, _ := writeModeFixture(t, "diff")
		rows := diffFixtureRows()
		rows[1].Selected = false
		rows[0].Selected = true
		tamperDiffRows(t, dir, rows)
		out, err := auditBatch(dir)
		if err == nil {
			t.Fatalf("a batch that switched the published attempt audited clean:\n%s", out)
		}
		if !strings.Contains(out, "solved: manifest 1, rows 0") {
			t.Fatalf("audit output does not name the moved verdict:\n%s", out)
		}
	})
}

// Diff mode never reads the in-flight snapshot a killed agent leaves, so the
// partial count is genuinely unknown. It must be absent from the manifest and
// printed as unknown — never as a zero the mode did not observe.
func TestDiffLeavesAnUnknowableCountAbsent(t *testing.T) {
	dir, m := writeModeFixture(t, "diff")
	if m.Trust.Claimed.Partial != nil {
		t.Fatalf("partial = %v, want absent: diff mode cannot observe it", *m.Trust.Claimed.Partial)
	}
	out, err := auditBatch(dir)
	if err != nil {
		t.Fatalf("a manifest with only an unmeasurable count absent did not audit: %v\n%s", err, out)
	}
	if !strings.Contains(out, "| partial | unknown | unknown | not claimed; not measured in this mode |") {
		t.Fatalf("audit does not print the unmeasurable count as unknown:\n%s", out)
	}
	if strings.Contains(out, "| partial | 0 |") {
		t.Fatalf("audit printed an unmeasured count as zero:\n%s", out)
	}
	for _, want := range []string{
		"denominator: **1 pull request**",
		"What this mode's audit does not re-derive",
		"does not re-run the comparison that chose it",
		"the published figures follow from the retained rows.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("diff audit output is missing %q:\n%s", want, out)
		}
	}
}

// Control: the already-covered suite mode's manifest content and audit output
// are unchanged by this change.
func TestSuiteManifestAndAuditAreUnchanged(t *testing.T) {
	dir, m := writeFixtureBatch(t)
	if m.Over.Command.Mode != "suite" {
		t.Fatalf("suite command mode = %q", m.Over.Command.Mode)
	}
	if m.Over.Denominator.Unit != "task" || m.Over.Denominator.Definition != denominatorDefinition || m.Over.Denominator.Value != 3 {
		t.Fatalf("suite denominator = %+v, want the unchanged task definition over 3 tasks", m.Over.Denominator)
	}
	if m.Trust.Claimed.Solved == nil || *m.Trust.Claimed.Solved != 3 || m.Trust.Claimed.Pass1 == nil || *m.Trust.Claimed.Pass1 != 2 {
		t.Fatalf("suite claims = %+v, want solved 3 / pass1 2", m.Trust.Claimed)
	}
	if m.Trust.Ledger.Planned == nil || *m.Trust.Ledger.Planned != 3 || *m.Trust.Ledger.Attempted != 4 || *m.Trust.Ledger.Excluded != 1 {
		t.Fatalf("suite ledger = %+v, want planned 3 / attempted 4 / excluded 1", m.Trust.Ledger)
	}
	out, err := auditBatch(dir)
	if err != nil {
		t.Fatalf("suite batch did not audit: %v\n%s", err, out)
	}
	for _, want := range []string{
		"| solved | 3 | 3 |",
		"| pass1 | 2 | 2 |",
		"| planned | 3 | n/a |",
		"the published figures follow from the retained rows.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("suite audit output changed: missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "does not re-derive") {
		t.Fatalf("suite audit gained a mode caveat section:\n%s", out)
	}
}

// -batch is accepted exactly by the modes that write one and by audit which
// reads one; every other mode keeps refusing it by name.
func TestBatchFlagBelongsToBatchModes(t *testing.T) {
	for _, mode := range []string{"suite", "swebench", "diff", "audit"} {
		if !modeTakesBatch(mode) {
			t.Fatalf("-batch should be accepted for mode %q", mode)
		}
	}
	for _, mode := range []string{"compare", "rollout", "traj", "barrier", "sft", "serve"} {
		if modeTakesBatch(mode) {
			t.Fatalf("-batch should be refused for mode %q: it writes no batch", mode)
		}
	}
}
