package stats

import (
	"fmt"
	"strings"
	"time"

	"reasonix/internal/contract/pricing"
)

// CardAudit keeps three questions apart: how many rows name a card, how many of
// those the published history can no longer resolve, and how many rows record no
// card at all. No fingerprint is unknown, never "today's card"; a fingerprint no
// published table matches is a real gap, because the card existed when the row
// was written but this build cannot name it.
type CardAudit struct {
	From string
	To   string
	// Known is false when the records could not be read. The counts are then
	// not zero: zero is a measurement, and an unreadable range measured nothing.
	Known bool
	Rows  int
	// Named counts rows carrying a pricing_fingerprint; Unresolvable is the
	// subset of those no published card matches.
	Named          int
	Unrecorded     int
	Unresolvable   int
	SupersededPeak int
	// Note explains an unresolvable count without implying general corruption.
	// Empty when nothing is unresolvable.
	Note string
}

// AuditCards reads the daily stats rows for the inclusive range and classifies
// the card each usage row names. Turn markers are not usage rows and are
// skipped. A missing stats directory is an empty range; an unreadable one is
// unknown, so Known is false and the caller must not render zeros.
func AuditCards(dir string, from, to time.Time) (CardAudit, error) {
	audit := CardAudit{From: from.Format(dayLayout), To: to.Format(dayLayout)}
	days := daysInRange(from, to)
	recordsByDay, err := readDailyRange(dir, days)
	if err != nil {
		return audit, err
	}
	audit.Known = true
	for _, day := range days {
		for _, rec := range recordsByDay[day] {
			if rec.Turn {
				continue
			}
			audit.Rows++
			name := strings.TrimSpace(rec.PricingFingerprint)
			if name == "" {
				audit.Unrecorded++
				continue
			}
			audit.Named++
			provider, model := splitModelRef(rec.ModelRef)
			resolved := pricing.ResolveCard(provider, model, rec.CostCurrency, name)
			if resolved.Found {
				continue
			}
			audit.Unresolvable++
			if resolved.SupersededPeak {
				audit.SupersededPeak++
			}
		}
	}
	audit.Note = cardAuditNote(audit)
	return audit, nil
}

// splitModelRef reads the canonical "provider/model" ref. A bare model name has
// no provider to resolve a card against, so it names a card that cannot be
// resolved rather than one that can be guessed.
func splitModelRef(modelRef string) (provider, model string) {
	ref := strings.TrimSpace(modelRef)
	if i := strings.IndexByte(ref, '/'); i > 0 && i+1 < len(ref) {
		return ref[:i], ref[i+1:]
	}
	return "", ref
}

// cardAuditNote names the one known reason a published card can be missing. A
// superseded peak card priced real turns and is recognisable as this project's,
// but the published history lists superseded base cards only, so the resolver
// cannot name it. Saying that plainly is the difference between a known gap and
// an implied corruption.
func cardAuditNote(a CardAudit) string {
	if !a.Known || a.Unresolvable == 0 {
		return ""
	}
	if a.SupersededPeak == 0 {
		return "no unresolvable card names a superseded peak card; none of them matches a card this project shipped."
	}
	if a.SupersededPeak == a.Unresolvable {
		return "every unresolvable card names a superseded peak card: the published history lists superseded base cards but not their peak cards, so this is a known gap in the card history, not corruption. Follow-up: publish superseded peak cards."
	}
	return fmt.Sprintf("%d of the %d unresolvable cards name a superseded peak card (a known gap in the published history); the rest match no card this project shipped. Follow-up: publish superseded peak cards.", a.SupersededPeak, a.Unresolvable)
}
