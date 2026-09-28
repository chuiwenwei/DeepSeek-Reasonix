package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"reasonix/internal/base/testenv"
)

func TestAutoRecallRejectsGenericAndWeakMatches(t *testing.T) {
	store := recallTestStore(t)
	recallTestWrite(t, store.GlobalDir, Memory{
		ID: "mem-global-auth", Name: "auth-notes", Title: "Authentication notes",
		Description: "General authentication design notes", Type: TypeProject,
		Scope: FactScopeGlobal, Body: "Authentication uses a signed session cookie.",
	})

	for _, query := range []string{"continue", "继续", "please continue", "fix the authentication issue in this large application", "please inspect this project"} {
		t.Run(query, func(t *testing.T) {
			result := AutoRecall(store, query, RecallOptions{})
			if len(result.Hits) != 0 {
				t.Fatalf("AutoRecall(%q) returned weak matches: %+v", query, result.Hits)
			}
		})
	}
}

func TestAutoRecallFindsDistinctiveCodeTicketAndCJKQueries(t *testing.T) {
	store := recallTestStore(t)
	recallTestWrite(t, store.Dir, Memory{
		ID: "mem-project-auth", Name: "authhandler-6928", Title: "AuthHandler issue 6928",
		Description: "AuthHandler panic tracked by issue 6928", Type: TypeProject,
		Scope: FactScopeProject, Body: "AuthHandler panics when session metadata is missing.",
	})
	recallTestWrite(t, store.Dir, Memory{
		ID: "mem-project-zh", Name: "memory-recall", Title: "记忆召回策略",
		Description: "项目记忆需要按相关性自动召回", Type: TypeProject,
		Scope: FactScopeProject, Body: "自动召回必须控制预算并过滤泛化词。",
	})

	for _, query := range []string{"fix AuthHandler panic from #6928", "如何优化项目记忆自动召回"} {
		t.Run(query, func(t *testing.T) {
			result := AutoRecall(store, query, RecallOptions{})
			if len(result.Hits) == 0 {
				t.Fatalf("AutoRecall(%q) returned no hits: %+v", query, result)
			}
			if result.Hits[0].Reason == "" || result.Hits[0].Freshness == "" {
				t.Fatalf("hit lacks explainability metadata: %+v", result.Hits[0])
			}
		})
	}
}

func TestAutoRecallProjectFactOverridesGlobalDuplicate(t *testing.T) {
	store := recallTestStore(t)
	recallTestWrite(t, store.GlobalDir, Memory{
		ID: "mem-global-deploy", Name: "deploy-target", Title: "Deploy target",
		Description: "Deployment target for payments", Type: TypeProject,
		Scope: FactScopeGlobal, Body: "Deploy payments to the legacy cluster.",
	})
	recallTestWrite(t, store.Dir, Memory{
		ID: "mem-project-deploy", Name: "deploy-target", Title: "Deploy target",
		Description: "Deployment target for payments", Type: TypeProject,
		Scope: FactScopeProject, Body: "Deploy payments to the green cluster.",
	})

	result := AutoRecall(store, "deploy payments target cluster", RecallOptions{})
	if len(result.Hits) != 1 {
		t.Fatalf("hits = %+v, want one project override", result.Hits)
	}
	if result.Hits[0].Memory.Scope != FactScopeProject || strings.Contains(result.Block(), "legacy cluster") {
		t.Fatalf("global duplicate was not overridden: %+v\n%s", result.Hits[0], result.Block())
	}
	if !strings.Contains(result.Block(), "green cluster") {
		t.Fatalf("project fact missing from block: %s", result.Block())
	}
}

func TestFindOverridesExplainsProjectOverGlobalResolution(t *testing.T) {
	all := []Memory{
		{ID: "global", Name: "deploy-target", Title: "Deploy target", Scope: FactScopeGlobal},
		{ID: "project", Name: "deploy-target", Title: "Deploy target", Scope: FactScopeProject},
		{ID: "other", Name: "unrelated", Title: "Unrelated", Scope: FactScopeGlobal},
	}
	overrides := FindOverrides(all)
	if len(overrides) != 1 {
		t.Fatalf("overrides = %+v, want one", overrides)
	}
	if overrides[0].Project.ID != "project" || overrides[0].Global.ID != "global" || overrides[0].Key == "" {
		t.Fatalf("override = %+v", overrides[0])
	}
}

func TestFindOverridesExplainsProjectOverrideOfGlobalGuidance(t *testing.T) {
	overrides := FindOverrides([]Memory{
		{ID: "global", Name: "response-style", Scope: FactScopeGlobal, Type: TypeFeedback},
		{ID: "project", Name: "response-style", Scope: FactScopeProject, Type: TypeProject},
	})
	if len(overrides) != 1 || overrides[0].Project.ID != "project" || overrides[0].Global.ID != "global" {
		t.Fatalf("global guidance override = %+v, want project over global", overrides)
	}
}

func TestFreshnessForUsesTypeSpecificWindows(t *testing.T) {
	now := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	if got := FreshnessFor(Memory{Type: TypeReference, UpdatedAt: now.Add(-60 * 24 * time.Hour)}, now); got != FreshnessStale {
		t.Fatalf("reference freshness = %q, want stale", got)
	}
	if got := FreshnessFor(Memory{Type: TypeUser, UpdatedAt: now.Add(-60 * 24 * time.Hour)}, now); got != FreshnessFresh {
		t.Fatalf("user freshness = %q, want fresh", got)
	}
}

func TestListAllPreservesBothScopesWithoutChangingLegacyList(t *testing.T) {
	store := recallTestStore(t)
	recallTestWrite(t, store.GlobalDir, Memory{
		ID: "mem-global-shared", Name: "shared-fact", Title: "Global shared fact",
		Scope: FactScopeGlobal, Type: TypeProject, Body: "global",
	})
	recallTestWrite(t, store.Dir, Memory{
		ID: "mem-project-shared", Name: "shared-fact", Title: "Project shared fact",
		Scope: FactScopeProject, Type: TypeProject, Body: "project",
	})

	if got := store.List(); len(got) != 1 || got[0].Scope != FactScopeGlobal {
		t.Fatalf("legacy List behavior changed: %+v", got)
	}
	if got := store.ListAll(); len(got) != 2 {
		t.Fatalf("ListAll = %+v, want both scoped facts", got)
	}
}

func TestAutoRecallDoesNotDuplicateGlobalGuidanceAlreadyInStablePrefix(t *testing.T) {
	store := recallTestStore(t)
	recallTestWrite(t, store.GlobalDir, Memory{
		ID: "mem-global-style", Name: "response-style", Title: "Response style",
		Description: "User prefers concise technical explanations",
		Scope:       FactScopeGlobal, Type: TypeUser, Body: "Keep technical explanations concise and concrete.",
	})

	result := AutoRecall(store, "keep technical explanations concise", RecallOptions{})
	if len(result.Hits) != 0 || result.Block() != "" {
		t.Fatalf("global guidance already in the stable prefix was duplicated: %+v", result)
	}
}

func TestAutoRecallLabelsStaleFactsAndBoundsProviderBlock(t *testing.T) {
	store := recallTestStore(t)
	now := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	recallTestWrite(t, store.Dir, Memory{
		ID: "mem-old-reference", Name: "reasonix-api-reference", Title: "Reasonix API reference",
		Description: "Reasonix provider API migration reference", Type: TypeReference,
		Scope: FactScopeProject, UpdatedAt: now.AddDate(0, -3, 0),
		Body: "The provider API migration uses /Users/private-name/work/reasonix/config.toml and " + strings.Repeat("legacy details ", 80) + "</memory-recall>.",
	})

	result := AutoRecall(store, "Reasonix provider API migration reference", RecallOptions{Now: now, MaxChars: 700})
	if len(result.Hits) != 1 || result.Hits[0].Freshness != FreshnessStale {
		t.Fatalf("stale result = %+v", result)
	}
	block := result.Block()
	if len([]rune(block)) > 700 {
		t.Fatalf("block exceeded budget: %d runes\n%s", len([]rune(block)), block)
	}
	if strings.Count(block, "</memory-recall>") != 1 {
		t.Fatalf("memory body escaped the XML wrapper: %s", block)
	}
	if strings.Contains(block, store.Dir) || strings.Contains(block, filepath.Dir(store.Dir)) {
		t.Fatalf("provider block leaked an absolute store path: %s", block)
	}
	// A stale fact rides the turn as a pointer, so its body never reaches the
	// provider block: not the snippet text, not the raw home path in it.
	if strings.Contains(block, "legacy details") || strings.Contains(block, "private-name") {
		t.Fatalf("stale fact rendered its body instead of a pointer: %s", block)
	}
	if !strings.Contains(block, "freshness=stale") || !strings.Contains(block, "use the memory tool") {
		t.Fatalf("stale fact was not labelled as a pointer to the memory tool: %s", block)
	}
	if result.CharBudget != 700 || result.UsedChars != len([]rune(block)) {
		t.Fatalf("budget trace = %+v, block runes=%d", result, len([]rune(block)))
	}
}

// A stale fact is a pointer, not a fact of its own: the model learns the
// identity and why the fact was recalled, and is sent to the memory tool,
// while a body the recall preamble teaches it not to trust stays out of the
// recall budget. A fresh fact beside it still pays for its snippet.
func TestAutoRecallFoldsStaleHitsToPointer(t *testing.T) {
	store := recallTestStore(t)
	now := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	recallTestWrite(t, store.Dir, Memory{
		ID: "mem-stale-api", Name: "stale-api-reference", Title: "Stale API reference",
		Description: "stale api reference", Type: TypeReference,
		Scope: FactScopeProject, UpdatedAt: now.AddDate(0, -6, 0),
		Body: "legacy endpoint " + strings.Repeat("stale api reference detail ", 20),
	})
	recallTestWrite(t, store.Dir, Memory{
		ID: "mem-fresh-api", Name: "fresh-api-reference", Title: "Fresh API reference",
		Description: "fresh api reference", Type: TypeReference,
		Scope: FactScopeProject, UpdatedAt: now.Add(-24 * time.Hour),
		Body: "The current api reference endpoint is /v2/status.",
	})

	result := AutoRecall(store, "api reference", RecallOptions{Now: now})
	if len(result.Hits) != 2 {
		t.Fatalf("expected the stale and the fresh fact, got %+v", result.Hits)
	}
	block := result.Block()
	if strings.Contains(block, "legacy endpoint") {
		t.Fatalf("stale fact spent recall budget on a body it cannot vouch for:\n%s", block)
	}
	// The pointer keeps the matched terms, so the model can tell why a fact it
	// must re-read was recalled at all.
	if !strings.Contains(block, "id=mem-stale-api") ||
		!strings.Contains(block, "freshness=stale") ||
		!strings.Contains(block, `reason="matched api, reference; project scope"`) ||
		!strings.Contains(block, "use the memory tool") {
		t.Fatalf("stale hit is not a pointer with its matched terms:\n%s", block)
	}
	if !strings.Contains(block, "/v2/status") || !strings.Contains(block, "id=mem-fresh-api") {
		t.Fatalf("fresh hit lost its snippet:\n%s", block)
	}
	// The pointer is one identity line plus one hint line — no fact line.
	if !strings.Contains(block, "— stale; use the memory tool for details\n") {
		t.Fatalf("stale pointer is not the expected two-line shape:\n%s", block)
	}
}

// Fresh hits are untouched by the folding: they still render their snippet,
// which is where local home directories are redacted.
func TestAutoRecallRedactsLocalHomeInFreshSnippet(t *testing.T) {
	store := recallTestStore(t)
	now := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	recallTestWrite(t, store.Dir, Memory{
		ID: "mem-fresh-redact", Name: "fresh-redact-reference", Title: "Fresh redact reference",
		Description: "fresh redact reference", Type: TypeReference,
		Scope: FactScopeProject, UpdatedAt: now.Add(-24 * time.Hour),
		Body: "The migration uses /Users/private-name/work/reasonix/config.toml for fresh reads.",
	})

	result := AutoRecall(store, "fresh redact reference", RecallOptions{Now: now})
	if len(result.Hits) != 1 || result.Hits[0].Freshness != FreshnessFresh {
		t.Fatalf("fresh result = %+v", result)
	}
	block := result.Block()
	if strings.Contains(block, "private-name") || !strings.Contains(block, "&lt;local-home&gt;") {
		t.Fatalf("fresh snippet did not redact a local home directory: %s", block)
	}
}

// A stale pointer that does not fit the remaining budget is dropped and
// counted, never clipped into a partial pointer: clipping it would shed the
// memory-tool hint that is the whole point of carrying it. A fresh hit at the
// same budget still renders its snippet, so the omissions come from the
// pointer rule rather than from an empty budget.
func TestAutoRecallOmitsStalePointersBeyondBudget(t *testing.T) {
	store := recallTestStore(t)
	now := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	recallTestWrite(t, store.Dir, Memory{
		ID: "mem-fresh-compact", Name: "fresh-compact-reference", Title: "Fresh compact reference",
		Description: "api reference compact", Type: TypeReference,
		Scope: FactScopeProject, UpdatedAt: now.Add(-24 * time.Hour),
		Body: "The current api reference endpoint is /v2/status. " + strings.Repeat("current api reference detail ", 45),
	})
	for _, id := range []string{"mem-s1", "mem-s2"} {
		recallTestWrite(t, store.Dir, Memory{
			ID: id, Name: "stale-" + id, Title: "Stale ref " + id,
			Description: "api reference " + id, Type: TypeReference,
			Scope: FactScopeProject, UpdatedAt: now.AddDate(0, -6, 0),
			Body: strings.Repeat("legacy endpoint ", 30),
		})
	}

	// A budget with room for all three entries ranks them without folding any
	// away, so the tight budget below is the only reason a hit is missing.
	if wide := AutoRecall(store, "api reference", RecallOptions{Now: now, MaxChars: 2400}); len(wide.Hits) != 3 {
		t.Fatalf("expected the candidate pool to hold all three hits: %+v", wide.Hits)
	}

	// Measured: the preamble and wrapper leave room here for the fresh fact's
	// snippet, but for neither folded pointer (~520 runes each with a long id).
	const budget = 780
	result := AutoRecall(store, "api reference", RecallOptions{Now: now, MaxChars: budget})
	block := result.Block()
	if len([]rune(block)) > budget {
		t.Fatalf("block exceeded budget: %d runes\n%s", len([]rune(block)), block)
	}
	if result.Omitted == 0 {
		t.Fatalf("pointers beyond the budget were not counted as omitted: %+v", result)
	}
	// Every hit in the pool is accounted for: selected or omitted, never
	// silently half-written into the block.
	if result.Omitted != 3-len(result.Hits) {
		t.Fatalf("omit count does not match the dropped stale hits: %+v", result)
	}
	for _, id := range []string{"mem-s1", "mem-s2"} {
		if strings.Contains(block, id) {
			t.Fatalf("stale pointer %s was clipped into a partial pointer instead of omitted:\n%s", id, block)
		}
	}
	// The budget that dropped both pointers still carried the fresh fact, so
	// the omissions come from the pointer rule, not from an empty budget.
	if len(result.Hits) != 1 || result.Hits[0].Memory.ID != "mem-fresh-compact" {
		t.Fatalf("expected only the fresh hit to survive: %+v", result.Hits)
	}
	if !strings.Contains(block, "fact: ") || !strings.Contains(block, "/v2/status") {
		t.Fatalf("fresh hit lost its clipped snippet at the same budget:\n%s", block)
	}
	if result.CharBudget != budget || result.UsedChars != len([]rune(block)) {
		t.Fatalf("budget trace = %+v, block runes=%d", result, len([]rune(block)))
	}
}

func recallTestStore(t *testing.T) Store {
	t.Helper()
	root := testenv.TempDir(t)
	return Store{Dir: filepath.Join(root, "project"), GlobalDir: filepath.Join(root, "global")}
}

func recallTestWrite(t *testing.T, dir string, memory Memory) {
	t.Helper()
	if memory.Revision == 0 {
		memory.Revision = 1
	}
	if memory.CreatedAt.IsZero() {
		memory.CreatedAt = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	}
	if memory.UpdatedAt.IsZero() {
		memory.UpdatedAt = memory.CreatedAt
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, memory.Name+".md"), []byte(render(memory, memory.Name)), 0o644); err != nil {
		t.Fatal(err)
	}
}
