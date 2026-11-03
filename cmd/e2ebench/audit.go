package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// runAuditMode prints both sides of the audit before returning the failure, so
// a moved figure is visible even when the exit code is non-zero.
func runAuditMode(dir, outMD string) error {
	report, err := auditBatch(dir)
	if report != "" {
		emit(report, outMD, "")
	}
	return err
}

// auditBatch re-derives the published figures from results.jsonl and checks
// them against batch.json. It reports both sides and never re-runs anything.
func auditBatch(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("audit needs -batch <directory>: the directory a batch run wrote")
	}
	manifestPath := filepath.Join(dir, batchManifestName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("no %s under %s: a batch that kept no manifest cannot be audited", batchManifestName, dir)
		}
		return "", fmt.Errorf("read %s: %w", batchManifestName, err)
	}
	var m batchManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return "", fmt.Errorf("%s is not valid JSON: %w", batchManifestName, err)
	}
	if m.SchemaVersion != batchSchemaVersion {
		return "", fmt.Errorf("%s declares schema version %d; this audit implements v%d", batchManifestName, m.SchemaVersion, batchSchemaVersion)
	}
	if commit := strings.TrimSpace(m.Producer.Commit); commit == "" || commit == "unknown" {
		return "", fmt.Errorf("%s records no commit (%q): the figures cannot be tied to a version, so this audit stops", batchManifestName, m.Producer.Commit)
	}

	rows, canonical, err := readBatchLines(filepath.Join(dir, batchResultsName))
	if err != nil {
		return "", err
	}
	hash := hashOf(canonical)
	expected, err := auditExpectations(m, rows)
	if err != nil {
		return "", err
	}
	failures := append([]string(nil), expected.extraFailures...)
	figures, claimFailures := compareClaims(m.Trust.Claimed, expected.claims, expected.requiredClaims)
	failures = append(failures, claimFailures...)
	ledger, ledgerFailures := compareLedger(m.Trust.Ledger, expected.ledger)
	failures = append(failures, ledgerFailures...)
	failures = append(failures, ledgerInvariants(m.Trust.Ledger)...)
	failures = append(failures, denominatorChecks(m, expected.ledger, expected.unit, expected.definition)...)
	if hash != m.Trust.ResultsSHA256 {
		failures = append([]string{fmt.Sprintf("results_sha256: manifest %s, rows %s", textOrUnknown(m.Trust.ResultsSHA256), hash)}, failures...)
	}
	report := renderAudit(dir, m, expected.rowCount, hash, figures, ledger, failures, expected.caveats)
	if len(failures) > 0 {
		return report, fmt.Errorf("%d mismatch(es): the published figures do not follow from the retained rows", len(failures))
	}
	if err := recordAuditReceipt(manifestPath, m, hash); err != nil {
		return report, err
	}
	return report, nil
}

// auditTarget is what a mode's own aggregation re-derives from the rows: the
// claimed figures, the ledger, the denominator's unit, and what this audit
// cannot re-derive for that mode.
type auditTarget struct {
	claims         batchClaims
	ledger         batchLedger
	unit           string
	definition     string
	requiredClaims map[string]bool
	caveats        []string
	extraFailures  []string
	rowCount       int
}

var (
	suiteRequiredClaims = map[string]bool{"solved": true, "pass1": true, "accounted": true, "unaccounted": true, "partial": true}
	// Diff mode never reads the in-flight snapshot a killed agent leaves, so it
	// cannot know the partial count: the claim is absent, and this audit must
	// read that as unknown rather than require a zero it did not observe.
	diffRequiredClaims = map[string]bool{"solved": true, "pass1": true, "accounted": true, "unaccounted": true}
)

// auditExpectations dispatches on the manifest's own mode. Each mode is checked
// with the aggregation that produced its report; a mode this audit does not
// implement is refused by name instead of checked against another mode's rules.
func auditExpectations(m batchManifest, rows []batchRow) (auditTarget, error) {
	mode := strings.TrimSpace(m.Over.Command.Mode)
	switch mode {
	case "suite", "swebench":
		results, err := decodeRows[result](rows)
		if err != nil {
			return auditTarget{}, err
		}
		stats := gatherSuiteStats(results)
		exp := auditTarget{
			claims:         observedClaimsFromStats(stats),
			ledger:         ledgerFromRows(results),
			requiredClaims: suiteRequiredClaims,
			rowCount:       len(results),
		}
		if mode == "swebench" {
			exp.unit, exp.definition = swebenchDenominatorUnit, swebenchDenominatorDefinition
			exp.caveats = []string{
				"the official harness's per-instance verdict is retained in each row (`passed`/`outcome`); the audit re-derives the published totals from those verdicts but does not re-run the grader",
				"the container's filesystem and the official test set are not retained, so a row's verdict can be checked against the grader report it came from, not re-executed",
			}
		} else {
			exp.unit, exp.definition = denominatorUnit, denominatorDefinition
		}
		return exp, nil
	case "diff":
		attempts, err := decodeRows[diffBatchRow](rows)
		if err != nil {
			return auditTarget{}, err
		}
		claims, ledger, _, selectionFailures := diffFigures(attempts)
		return auditTarget{
			claims: claims, ledger: ledger,
			unit: diffDenominatorUnit, definition: diffDenominatorDefinition,
			requiredClaims: diffRequiredClaims,
			caveats: []string{
				"the attempt chosen for the published verdict is retained in the row's `selected` flag; the audit checks that exactly one attempt is selected and that the verdict follows from it, but does not re-run the comparison that chose it",
				"`partial` is not measured in diff mode (no in-flight snapshot is read), so an absent partial count is unknown, never zero",
			},
			extraFailures: selectionFailures,
			rowCount:      len(attempts),
		}, nil
	default:
		return auditTarget{}, fmt.Errorf("%s records mode %q, which this audit does not implement; the batch cannot be checked by another mode's rules", batchManifestName, mode)
	}
}

// batchRow is one line of results.jsonl, kept raw so the mode decides its type.
type batchRow struct {
	line int
	raw  []byte
}

// readBatchLines parses results.jsonl once: the raw rows each mode decodes
// with its own type, and the canonical bytes results_sha256 covers.
func readBatchLines(path string) ([]batchRow, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, fmt.Errorf("no %s: the manifest describes rows that are not beside it", batchResultsName)
		}
		return nil, nil, fmt.Errorf("read %s: %w", batchResultsName, err)
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		return nil, nil, fmt.Errorf("%s does not end in a newline: its last row is truncated", batchResultsName)
	}
	var rows []batchRow
	var canonical bytes.Buffer
	line := 0
	for raw := range bytes.SplitSeq(data, []byte("\n")) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		line++
		if !json.Valid(raw) {
			return nil, nil, fmt.Errorf("%s line %d is not a complete row (truncated or corrupt)", batchResultsName, line)
		}
		rows = append(rows, batchRow{line: line, raw: append([]byte(nil), raw...)})
		canon, err := canonicalJSON(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("%s line %d: %w", batchResultsName, line, err)
		}
		canonical.Write(canon)
		canonical.WriteByte('\n')
	}
	return rows, canonical.Bytes(), nil
}

// decodeRows decodes the retained lines as the mode's row type. A row that
// does not decode is a truncated or corrupt row, never an empty one.
func decodeRows[T any](rows []batchRow) ([]T, error) {
	out := make([]T, 0, len(rows))
	for _, row := range rows {
		var v T
		if err := json.Unmarshal(row.raw, &v); err != nil {
			return nil, fmt.Errorf("%s line %d is not a complete row (truncated or corrupt): %w", batchResultsName, row.line, err)
		}
		out = append(out, v)
	}
	return out, nil
}

// readBatch parses results.jsonl once: the typed rows the figures come from,
// and the canonical bytes results_sha256 covers.
func readBatch(path string) ([]result, []byte, error) {
	rows, canonical, err := readBatchLines(path)
	if err != nil {
		return nil, nil, err
	}
	typed, err := decodeRows[result](rows)
	if err != nil {
		return nil, nil, err
	}
	return typed, canonical, nil
}

// observedClaimsFromStats is what the rows say, with no omission rules: the
// audit must see a zero where the rows measured zero, so it can tell that
// apart from a figure the mode never measured.
func observedClaimsFromStats(s suiteStats) batchClaims {
	c := batchClaims{
		Solved: new(s.passed), Pass1: new(s.pass1),
		PromptTokens: new(s.pTok), CompletionTokens: new(s.cTok),
		CacheHitTokens: new(s.hit), CacheMissTokens: new(s.miss),
		Cost: new(s.cost), Currency: s.currency,
		Accounted: new(s.accounted), Unaccounted: new(s.unaccounted), Partial: new(s.partial),
	}
	if len(s.ttcs) > 0 {
		c.TCTSMedianMs = new(median(s.ttcs))
	}
	return c
}

// auditFigure is one number on both sides: what the manifest claimed and what
// the retained rows give. Unknown is printed as unknown, never as zero.
type auditFigure struct {
	name    string
	claimed string
	actual  string
	note    string
	moved   bool
}

func compareClaims(c, observed batchClaims, required map[string]bool) ([]auditFigure, []string) {
	rows := []struct {
		name    string
		claimed string
		actual  string
	}{
		{"solved", intText(c.Solved), intText(observed.Solved)},
		{"pass1", intText(c.Pass1), intText(observed.Pass1)},
		{"prompt_tokens", intText(c.PromptTokens), intText(observed.PromptTokens)},
		{"completion_tokens", intText(c.CompletionTokens), intText(observed.CompletionTokens)},
		{"cache_hit_tokens", intText(c.CacheHitTokens), intText(observed.CacheHitTokens)},
		{"cache_miss_tokens", intText(c.CacheMissTokens), intText(observed.CacheMissTokens)},
		{"cost", floatText(c.Cost), floatText(observed.Cost)},
		{"currency", c.Currency, observed.Currency},
		{"accounted", intText(c.Accounted), intText(observed.Accounted)},
		{"unaccounted", intText(c.Unaccounted), intText(observed.Unaccounted)},
		{"partial", intText(c.Partial), intText(observed.Partial)},
		{"tcts_median_ms", int64Text(c.TCTSMedianMs), int64Text(observed.TCTSMedianMs)},
	}
	var figures []auditFigure
	var failures []string
	for _, row := range rows {
		f := auditFigure{name: row.name, claimed: row.claimed, actual: row.actual}
		switch {
		case row.claimed == "":
			f.claimed = "unknown"
			f.note = "not claimed"
			if required[row.name] {
				f.note = "required, absent"
				failures = append(failures, row.name+": the manifest claims no "+row.name+"; an absent figure is required here")
			}
			if row.actual == "" {
				f.actual = "unknown"
				f.note = joinNote(f.note, "not measured in this mode")
			}
		case row.actual == "":
			// The manifest publishes a figure its rows do not carry: that is a
			// claim, not a measurement, and it cannot follow from the rows.
			f.actual = "unknown"
			f.moved = true
			failures = append(failures, row.name+": the manifest claims a figure these rows do not measure")
		case row.claimed != row.actual:
			f.moved = true
			failures = append(failures, fmt.Sprintf("%s: manifest %s, rows %s", row.name, row.claimed, row.actual))
		}
		figures = append(figures, f)
	}
	return figures, failures
}

// compareLedger checks the counts the rows can answer. Planned cannot be
// recomputed from rows — it is what the batch set out to do — so it is only
// held to the consistency checks in ledgerInvariants.
func compareLedger(declared, actual batchLedger) ([]auditFigure, []string) {
	type entry struct {
		name     string
		claimed  *int
		replayed *int
	}
	entries := []entry{
		{"planned", declared.Planned, nil},
		{"attempted", declared.Attempted, actual.Attempted},
		{"completed", declared.Completed, actual.Completed},
		{"scored", declared.Scored, actual.Scored},
		{"failed", declared.Failed, actual.Failed},
		{"unmeasured", declared.Unmeasured, actual.Unmeasured},
		{"excluded", declared.Excluded, actual.Excluded},
		{"rows", declared.Rows, actual.Rows},
		{"skipped", declared.Skipped, actual.Skipped},
	}
	required := map[string]bool{"planned": true, "attempted": true, "completed": true, "scored": true, "failed": true, "unmeasured": true, "excluded": true}
	var figures []auditFigure
	var failures []string
	for _, e := range entries {
		f := auditFigure{name: e.name}
		switch {
		case e.claimed != nil:
			f.claimed = strconv.Itoa(*e.claimed)
		default:
			f.claimed = "unknown"
			f.note = "not claimed"
			if required[e.name] {
				f.note = "required, absent"
				failures = append(failures, "ledger."+e.name+": absent; schema v1 requires this count, and absent is unknown, never zero")
			}
		}
		if e.replayed == nil {
			f.actual = "n/a"
			f.note = joinNote(f.note, "not recomputable from rows")
		} else {
			f.actual = strconv.Itoa(*e.replayed)
		}
		if e.claimed != nil && e.replayed != nil && *e.claimed != *e.replayed {
			f.moved = true
			failures = append(failures, fmt.Sprintf("ledger.%s: manifest %d, rows %d", e.name, *e.claimed, *e.replayed))
		}
		figures = append(figures, f)
	}
	return figures, failures
}

// ledgerInvariants is the denominator guard: the counts must describe a
// funnel, so a manifest cannot publish a numerator larger than its base.
func ledgerInvariants(l batchLedger) []string {
	var out []string
	check := func(lower, upper *int, lowerName, upperName string) {
		if lower == nil || upper == nil || *lower <= *upper {
			return
		}
		out = append(out, fmt.Sprintf("ledger: %s %d exceeds %s %d", lowerName, *lower, upperName, *upper))
	}
	check(l.Scored, l.Planned, "scored", "planned")
	check(l.Completed, l.Attempted, "completed", "attempted")
	check(l.Scored, l.Completed, "scored", "completed")
	check(l.Failed, l.Scored, "failed", "scored")
	check(l.Unmeasured, l.Completed, "unmeasured", "completed")
	check(l.Excluded, l.Rows, "excluded", "rows")
	check(l.Unmeasured, l.Attempted, "unmeasured", "attempted")
	if l.Rows != nil && l.Attempted != nil && l.Skipped != nil && *l.Rows != *l.Attempted+*l.Skipped {
		out = append(out, fmt.Sprintf("ledger: rows %d is not attempted %d + skipped %d", *l.Rows, *l.Attempted, *l.Skipped))
	}
	return out
}

// denominatorChecks ties the published denominator to the ledger and to the
// outcome split, so passes and failures have to add up to it. The unit and
// definition are the mode's own: a task count cannot stand in for instances or
// for one PR.
func denominatorChecks(m batchManifest, fromRows batchLedger, unit, definition string) []string {
	var out []string
	d := m.Over.Denominator
	if d.Unit != unit || d.Definition != definition {
		out = append(out, "denominator: the manifest defines it as "+strconv.Quote(d.Unit+" / "+d.Definition)+", which this audit does not implement")
	}
	if fromRows.Scored != nil && d.Value != *fromRows.Scored {
		out = append(out, fmt.Sprintf("denominator: manifest %d, rows %d", d.Value, *fromRows.Scored))
	}
	if m.Trust.Ledger.Scored != nil && d.Value != *m.Trust.Ledger.Scored {
		out = append(out, fmt.Sprintf("denominator: manifest %d, ledger.scored %d", d.Value, *m.Trust.Ledger.Scored))
	}
	if m.Trust.Claimed.Pass1 != nil && m.Trust.Ledger.Failed != nil && m.Trust.Ledger.Scored != nil {
		if sum := *m.Trust.Claimed.Pass1 + *m.Trust.Ledger.Failed; sum != *m.Trust.Ledger.Scored {
			out = append(out, fmt.Sprintf("ledger: scored %d is not pass1 %d + failed %d", *m.Trust.Ledger.Scored, *m.Trust.Claimed.Pass1, *m.Trust.Ledger.Failed))
		}
	}
	return out
}

// recordAuditReceipt keeps the verifier identity in the manifest, so a later
// re-verification can tell which code checked it.
func recordAuditReceipt(manifestPath string, m batchManifest, hash string) error {
	m.Audit = &batchAuditReceipt{
		VerifierVersion: auditToolVersion,
		VerifiedAt:      time.Now().UTC().Format(time.RFC3339),
		ResultsSHA256:   hash,
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode audit receipt: %w", err)
	}
	if err := os.WriteFile(manifestPath, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("record audit receipt: %w", err)
	}
	return nil
}

func renderAudit(dir string, m batchManifest, rowCount int, hash string, figures, ledger []auditFigure, failures, caveats []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## e2ebench batch audit — `%s`\n\n", filepath.Base(strings.TrimSpace(dir)))
	b.WriteString("**What the figures are over**\n\n")
	fmt.Fprintf(&b, "- schema: manifest v%d, audited by `%s/%d`\n", m.SchemaVersion, auditToolName, auditToolVersion)
	fmt.Fprintf(&b, "- command: `%s` · axes: %s\n", m.Over.Command.Mode, axisText(m.Over.Command.Axes))
	fmt.Fprintf(&b, "- %s: `%s` (%d ids; the text is represented by this digest, not stored)\n", unitLabel(m.Over.Command.Mode), textOrUnknown(m.Over.Tasks.Digest), m.Over.Tasks.Count)
	fmt.Fprintf(&b, "- filters: %s\n", filterTextFor(m.Over.Command.Mode, m.Over.Filters))
	fmt.Fprintf(&b, "- denominator: **%d %s** — %s\n", m.Over.Denominator.Value, denominatorText(m.Over.Denominator.Unit, m.Over.Denominator.Value), m.Over.Denominator.Definition)
	b.WriteString("\n**Which run produced them**\n\n")
	fmt.Fprintf(&b, "- commit: `%s` (%s, %s)\n", m.Producer.Commit, sourceText(m.Producer.CommitSource), dirtyText(m.Producer.Dirty))
	fmt.Fprintf(&b, "- model: %s · provider: %s\n", nameText(m.Producer.Model), nameText(m.Producer.Provider))
	fmt.Fprintf(&b, "- seed: %s\n", seedText(m.Producer.Seed))
	fmt.Fprintf(&b, "- started: %s · finished: %s\n", m.Producer.StartedAt, m.Producer.FinishedAt)
	fmt.Fprintf(&b, "- harness: %s · runtime %s\n", m.Producer.HarnessVersion, m.Producer.GoVersion)
	b.WriteString("\n**Why they can still be trusted**\n\n")
	fmt.Fprintf(&b, "- rows retained: %d in `%s`\n", rowCount, batchResultsName)
	fmt.Fprintf(&b, "- results_sha256: %s\n", hashText(m.Trust.ResultsSHA256, hash))
	if m.Audit != nil {
		fmt.Fprintf(&b, "- last verified by `%s/%d` at %s over `%s`\n", auditToolName, m.Audit.VerifierVersion, m.Audit.VerifiedAt, m.Audit.ResultsSHA256)
	}
	fmt.Fprintf(&b, "- retention: %s\n", textOrUnknown(m.Trust.Retention))
	b.WriteString("\n| claimed figure | manifest | rows | |\n|---|---:|---:|---|\n")
	writeFigures(&b, figures)
	b.WriteString("\n| ledger | manifest | rows | |\n|---|---:|---:|---|\n")
	writeFigures(&b, ledger)
	b.WriteString("\n")
	if len(failures) > 0 {
		b.WriteString("**Mismatches**\n\n")
		for _, f := range failures {
			fmt.Fprintf(&b, "- %s\n", f)
		}
		b.WriteString("\n")
	}
	if len(caveats) > 0 {
		b.WriteString("**What this mode's audit does not re-derive**\n\n")
		for _, c := range caveats {
			fmt.Fprintf(&b, "- %s\n", c)
		}
		b.WriteString("\n")
	}
	if len(failures) > 0 {
		fmt.Fprintf(&b, "**Verdict:** the published figures do **not** follow from the retained rows (%d mismatch(es)).\n\n", len(failures))
	} else {
		b.WriteString("**Verdict:** the published figures follow from the retained rows.\n\n")
	}
	b.WriteString("<sub>An aggregation audit, not a reproduction: the model, tools and environment are not re-executed, so this cannot show the experiment would reproduce. A real replay would need the commit, corpus fingerprint, model and provider configuration, seed, flags, raw results and runtime version; it is not implemented.</sub>\n")
	return b.String()
}

// denominatorText pluralizes the unit without changing the suite's existing
// line ("3 tasks"): only the PR unit has a singular form worth printing.
func denominatorText(unit string, n int) string {
	if unit == diffDenominatorUnit && n == 1 {
		return unit
	}
	return unit + "s"
}

// unitLabel names what over.tasks fingerprints, so a SWE-bench instance set is
// not printed as task directories.
func unitLabel(mode string) string {
	switch mode {
	case "swebench":
		return "instance ids"
	case "diff":
		return "changed source files"
	default:
		return "task ids"
	}
}

func writeFigures(b *strings.Builder, figures []auditFigure) {
	for _, f := range figures {
		note := f.note
		if f.moved {
			note = joinNote(note, "**moved**")
		}
		fmt.Fprintf(b, "| %s | %s | %s | %s |\n", f.name, f.claimed, f.actual, note)
	}
}

func axisText(axes []batchFlag) string {
	if len(axes) == 0 {
		return "none recorded"
	}
	parts := make([]string, len(axes))
	for i, a := range axes {
		parts[i] = a.Name + "=" + a.Value
	}
	return "`" + strings.Join(parts, "` · `") + "`"
}

// filterTextFor keeps the suite's wording and stops a SWE-bench or diff audit
// from claiming a -task filter applies to it.
func filterTextFor(mode string, f batchFilters) string {
	if mode == "" || mode == "suite" {
		return filterText(f)
	}
	if strings.TrimSpace(f.Task) == "" {
		return "none in this mode"
	}
	return "`-task " + f.Task + "`"
}

func filterText(f batchFilters) string {
	if strings.TrimSpace(f.Task) == "" {
		return "none (`-task` unset: every task in the suite)"
	}
	return "`-task " + f.Task + "`"
}

func sourceText(source string) string {
	if source == "" {
		return "source unknown"
	}
	return "source " + source
}

func dirtyText(dirty *bool) string {
	if dirty == nil {
		return "dirty state unknown"
	}
	if *dirty {
		return "dirty tree: the commit does not identify the code that ran"
	}
	return "clean tree"
}

// nameText prints an identity name, and says unknown when the field is absent
// rather than printing an empty name that reads as "none".
func nameText(name string) string {
	if strings.TrimSpace(name) == "" {
		return "unknown (absent from the manifest; absent is not zero)"
	}
	return "`" + name + "`"
}

func seedText(seed *int64) string {
	if seed == nil {
		return "unknown (absent from the manifest; an absent seed is not seed 0)"
	}
	return strconv.FormatInt(*seed, 10)
}

func hashText(claimed, actual string) string {
	if claimed == "" {
		return "unknown (the manifest claims no digest)"
	}
	if claimed != actual {
		return "**MISMATCH** — manifest `" + claimed + "`, rows `" + actual + "`"
	}
	return "ok (`" + actual + "`)"
}

func textOrUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}

func joinNote(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "; " + b
	}
}

func intText(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

func int64Text(p *int64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatInt(*p, 10)
}

func floatText(p *float64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatFloat(*p, 'f', -1, 64)
}
