package main

import (
	"strconv"
	"time"
)

// Each batch-producing mode supplies its own provenance, denominator and
// aggregation; buildManifest and writeBatchRows keep the field groups identical.

// swebenchAxes lists what changes what a SWE-bench figure means. The subset
// path is absent (the instance ids are digested into over.tasks instead) and
// the network/proxy flags are absent because they are endpoints, not evidence.
func swebenchAxes(o swebenchOpts) []batchFlag {
	return []batchFlag{
		{"profile", o.profile},
		{"ablate", o.arm.Arm()},
		{"permission", valueOr(o.permission, "auto")},
		{"dataset", o.dataset},
		{"max-steps", strconv.Itoa(o.maxSteps)},
		{"timeout", strconv.Itoa(o.timeoutSec)},
	}
}

// captureSwebenchProvenance reads the checkout and model identity before the
// first instance runs, for the same reason suite mode does: git cannot
// describe a tree that has since been touched.
func captureSwebenchProvenance(o swebenchOpts, instances []swebenchInstance) batchProvenance {
	commit, dirty := checkoutCommit(".")
	model, provider := modelIdentity(o.model)
	ids := make([]string, len(instances))
	for i, inst := range instances {
		ids[i] = inst.InstanceID
	}
	count, digest := idSetDigest(ids)
	return batchProvenance{
		dir: o.batchDir, mode: "swebench", started: time.Now().UTC(), planned: count,
		axes: swebenchAxes(o), tasks: batchTaskSet{Count: count, Digest: digest},
		commit: commit, dirty: dirty, model: model, provider: provider,
	}
}

// buildSwebenchManifest publishes the figures the official-grader report
// publishes, over instances rather than this repo's task directories.
func buildSwebenchManifest(p batchProvenance, results []result, resultsSHA256 string) batchManifest {
	stats := gatherSuiteStats(results)
	ledger := ledgerFor(results, p.planned)
	ledger.Definitions = swebenchLedgerDefinitions
	return buildManifest(p,
		batchDenominator{Unit: swebenchDenominatorUnit, Definition: swebenchDenominatorDefinition, Value: stats.ran},
		claimsFor(stats), ledger, resultsSHA256)
}

// writeSwebenchBatch retains one row per instance beside the manifest. The
// rows are result rows, but the manifest is built by this mode's builder: a
// suite denominator ("task") must never stand in for instances.
func writeSwebenchBatch(p batchProvenance, results []result) error {
	rows := make([]any, len(results))
	for i := range results {
		rows[i] = results[i]
	}
	return writeBatchRows(p.dir, rows, func(resultsSHA256 string) batchManifest {
		return buildSwebenchManifest(p, results, resultsSHA256)
	})
}

// diffAxes lists what changes what a diff-mode figure means. -repo is a path
// and -test-cmd is free text, so neither is copied into the manifest.
func diffAxes(o diffOpts) []batchFlag {
	return []batchFlag{
		{"profile", o.profile},
		{"ablate", o.ablate.Arm()},
		{"base", o.base},
		{"max-steps", strconv.Itoa(o.maxSteps)},
		{"timeout", strconv.Itoa(o.timeoutSec)},
		{"attempts", strconv.Itoa(max(o.attempts, 1))},
	}
}

// captureDiffProvenance records the PR the batch is about, before the agent
// touches the tree. The changed files are digested, never stored.
func captureDiffProvenance(o diffOpts, srcFiles []string) batchProvenance {
	commit, dirty := checkoutCommit(o.repo)
	model, provider := modelIdentity(o.model)
	count, digest := idSetDigest(srcFiles)
	return batchProvenance{
		dir: o.batchDir, mode: "diff", started: time.Now().UTC(),
		axes: diffAxes(o), tasks: batchTaskSet{Count: count, Digest: digest},
		commit: commit, dirty: dirty, model: model, provider: provider,
	}
}

// diffBatchRow is one generation attempt as retained evidence. It carries the
// figures the diff report publishes, and Selected marks which attempt the
// report rendered so the audit never has to re-implement the choice.
type diffBatchRow struct {
	Attempt          int     `json:"attempt"`
	Selected         bool    `json:"selected"`
	Passed           bool    `json:"passed"`
	TestsPass        bool    `json:"tests_pass"`
	NewTests         int     `json:"new_tests"`
	Pins             int     `json:"pins"`
	AssertionPins    int     `json:"assertion_pins"`
	AddedTestLines   int     `json:"added_test_lines"`
	Covered          int     `json:"covered"`
	CoverTotal       int     `json:"cover_total"`
	MutationCaught   int     `json:"mutation_caught"`
	MutationTotal    int     `json:"mutation_total"`
	BuildOK          bool    `json:"build_ok"`
	SourceTouched    int     `json:"source_touched"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	CacheHitTokens   int     `json:"cache_hit_tokens"`
	CacheMissTokens  int     `json:"cache_miss_tokens"`
	Cost             float64 `json:"cost"`
	Currency         string  `json:"currency,omitempty"`
	// Unaccounted marks an attempt whose metrics file never landed, so its
	// cost and tokens are unknown rather than zero.
	Unaccounted bool   `json:"unaccounted"`
	Note        string `json:"note,omitempty"`
}

func newDiffBatchRow(r diffReport, attempt int) diffBatchRow {
	return diffBatchRow{
		Attempt: attempt, Passed: r.passed, TestsPass: r.testsPass,
		NewTests: len(r.newTests), Pins: countPins(r.pins), AssertionPins: countAssertionPins(r.pins),
		AddedTestLines: r.addedTestLines, Covered: r.covered, CoverTotal: r.coverTotal,
		MutationCaught: r.mut.caught, MutationTotal: r.mut.total,
		BuildOK: r.buildOK, SourceTouched: r.sourceTouched,
		PromptTokens: r.m.PromptTokens, CompletionTokens: r.m.CompletionTokens,
		CacheHitTokens: r.m.CacheHitTokens, CacheMissTokens: r.m.CacheMissTokens,
		Cost: r.m.Cost, Currency: r.m.Currency,
		Unaccounted: r.unaccounted, Note: diffRowNote(r),
	}
}

// diffRowNote keeps the attempt's failure reason, bounded like a grader note,
// so a row explains itself without carrying the agent's output.
func diffRowNote(r diffReport) string {
	switch {
	case r.runErr != nil:
		return utf8Prefix("run: "+r.runErr.Error(), graderNoteLimit)
	case r.analysisErr != nil:
		return utf8Prefix("analysis: "+r.analysisErr.Error(), graderNoteLimit)
	}
	return ""
}

// diffFigures re-derives the published figures from the retained attempts with
// the mode's own aggregation: the verdict is the selected attempt's, the spend
// sums every accounted attempt, and the ledger names the PR as the denominator.
// A mode-specific failure (no selected attempt, or more than one) is returned
// rather than papered over.
func diffFigures(rows []diffBatchRow) (batchClaims, batchLedger, int, []string) {
	var failures []string
	selected := -1
	for i := range rows {
		if !rows[i].Selected {
			continue
		}
		if selected >= 0 {
			failures = append(failures, "selected: more than one retained attempt is marked as the published one")
			break
		}
		selected = i
	}
	if len(rows) > 0 && selected < 0 {
		failures = append(failures, "selected: no retained attempt is marked as the published one")
	}
	solved := 0
	if selected >= 0 && rows[selected].Passed {
		solved = 1
	}

	accounted, unaccounted := 0, 0
	var pTok, cTok, hit, miss int
	var cost float64
	var currency string
	for _, r := range rows {
		if r.Unaccounted {
			unaccounted++
			continue
		}
		accounted++
		pTok += r.PromptTokens
		cTok += r.CompletionTokens
		hit += r.CacheHitTokens
		miss += r.CacheMissTokens
		cost += r.Cost
		if r.Currency != "" {
			currency = r.Currency
		}
	}
	claims := batchClaims{
		Solved: new(solved), Pass1: new(solved),
		Accounted: new(accounted), Unaccounted: new(unaccounted),
		// Partial is genuinely unknown here: diff mode never reads the
		// in-flight snapshot a killed agent leaves, so the count is absent
		// rather than a zero it did not observe.
	}
	if accounted > 0 {
		claims.PromptTokens, claims.CompletionTokens = new(pTok), new(cTok)
		claims.Cost, claims.Currency = new(cost), currency
		if hit+miss > 0 {
			claims.CacheHitTokens, claims.CacheMissTokens = new(hit), new(miss)
		}
	}

	scored := 0
	if len(rows) > 0 {
		scored = 1
	}
	ledger := batchLedger{
		Planned: new(len(rows)), Attempted: new(len(rows)), Completed: new(len(rows)),
		Scored: new(scored), Failed: new(scored - solved),
		Unmeasured: new(unaccounted), Excluded: new(len(rows) - scored),
		Rows: new(len(rows)), Skipped: new(0),
		Definitions: diffLedgerDefinitions,
	}
	return claims, ledger, scored, failures
}

func buildDiffManifest(p batchProvenance, rows []diffBatchRow, resultsSHA256 string) batchManifest {
	claims, ledger, scored, _ := diffFigures(rows)
	return buildManifest(p,
		batchDenominator{Unit: diffDenominatorUnit, Definition: diffDenominatorDefinition, Value: scored},
		claims, ledger, resultsSHA256)
}

// writeDiffBatch retains one row per attempt beside the manifest. With no
// attempt (a PR with no Go source changes) there is nothing to retain, and the
// caller says so instead of writing a manifest with no evidence behind it.
func writeDiffBatch(p batchProvenance, rows []diffBatchRow) error {
	marshal := make([]any, len(rows))
	for i := range rows {
		marshal[i] = rows[i]
	}
	return writeBatchRows(p.dir, marshal, func(resultsSHA256 string) batchManifest {
		return buildDiffManifest(p, rows, resultsSHA256)
	})
}
