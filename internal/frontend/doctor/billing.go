package doctor

import (
	"fmt"
	"reasonix/internal/contract/pricing"
	"strings"
	"time"

	"reasonix/internal/contract/config"
	"reasonix/internal/state/stats"
)

// BillingReport is the structured output of `reasonix doctor billing`.
type BillingReport struct {
	DisplayCurrencyPref string                `json:"display_currency_pref"`
	DisplayCurrency     string                `json:"display_currency"`
	FX                  FXReport              `json:"fx"`
	Providers           []ProviderBillingInfo `json:"providers"`
	CatalogNotes        []string              `json:"catalog_notes,omitempty"`
	// CardAudit reads the stored rows in the selected range back against the
	// published card history. It is nil only when the caller asked for provider
	// diagnostics alone; the CLI always fills it.
	CardAudit *CardAuditReport `json:"card_audit,omitempty"`
}

// CardAuditReport is how the stored rows in a range account for the rate cards
// that priced them. Named counts rows carrying a pricing_fingerprint and
// Unresolvable is the subset no published card matches; Unrecorded counts rows
// with no fingerprint at all. The three counts are pointers so an unreadable
// range serializes as absent (unknown) instead of a measured zero.
type CardAuditReport struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Known is false when the stats records could not be read. The counts are
	// then omitted, because the report cannot tell and must not claim zero.
	Known          bool   `json:"known"`
	Rows           int    `json:"rows"`
	Named          *int   `json:"named,omitempty"`
	Unresolvable   *int   `json:"unresolvable,omitempty"`
	Unrecorded     *int   `json:"unrecorded,omitempty"`
	SupersededPeak *int   `json:"superseded_peak,omitempty"`
	Note           string `json:"note,omitempty"`
}

// BillingOptions selects the stored-row range the billing report audits.
type BillingOptions struct {
	// From and To select inclusive calendar days. A zero pair means the last
	// Days days ending at Now (local time).
	From, To time.Time
	// Days is the trailing window when From/To are zero. Zero means 30.
	Days int
	// Now anchors a trailing window; zero means time.Now().
	Now time.Time
	// StatsDir overrides config.StatsDir(). Empty reads the configured root.
	StatsDir string
}

// FXReport is retained for JSON compatibility. Runtime FX is intentionally
// disabled; old readers may continue to expect the object.
type FXReport struct {
	Enabled     bool      `json:"enabled"`
	Source      string    `json:"source"`
	CachePath   string    `json:"cache_path,omitempty"`
	FetchedAt   time.Time `json:"fetched_at,omitempty"`
	Stale       bool      `json:"stale"`
	HasTable    bool      `json:"has_table"`
	Observation string    `json:"latest_observation,omitempty"`
}

// ProviderBillingInfo describes one provider's frozen list-price currency.
type ProviderBillingInfo struct {
	Name            string  `json:"name"`
	Model           string  `json:"model,omitempty"`
	BillingCurrency string  `json:"billing_currency,omitempty"`
	BillingMode     string  `json:"billing_mode,omitempty"`
	PriceCurrency   string  `json:"price_currency,omitempty"`
	PriceInput      float64 `json:"price_input,omitempty"`
	PriceOutput     float64 `json:"price_output,omitempty"`
	PriceCacheHit   float64 `json:"price_cache_hit,omitempty"`
	CatalogMatch    bool    `json:"catalog_match"`
	CatalogSource   string  `json:"catalog_source,omitempty"`
	Fingerprint     string  `json:"pricing_fingerprint,omitempty"`
	CustomPrice     bool    `json:"custom_price"`
}

// CollectBillingWithOptions builds a billing diagnostics report and audits the
// stored rows in opts' range. The report is read-only: it names the cards the
// rows carry and never reprices an amount or rewrites a rate table.
func CollectBillingWithOptions(cfg *config.Config, opts BillingOptions) BillingReport {
	if cfg == nil {
		cfg = config.Default()
	}
	rep := BillingReport{
		DisplayCurrencyPref: prefLabel(cfg.DisplayCurrencyPref()),
		DisplayCurrency:     cfg.ExplicitDisplayCurrency(),
		FX: FXReport{
			Enabled: false,
			Source:  "disabled",
		},
		CardAudit: collectCardAudit(opts),
	}

	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		info := ProviderBillingInfo{
			Name:            p.Name,
			Model:           p.Model,
			BillingCurrency: p.ProviderBillingCurrency(),
			BillingMode:     p.ProviderBillingMode(),
		}
		price := p.PriceForModel(p.Model)
		if price != nil {
			info.PriceCurrency = pricing.NormalizeCurrency(price.Currency)
			info.PriceInput = price.Input
			info.PriceOutput = price.Output
			info.PriceCacheHit = price.CacheHit
			card := pricing.RateCard{
				CacheHit: price.CacheHit, Input: price.Input, Output: price.Output,
				Currency: info.PriceCurrency,
			}
			info.Fingerprint = pricing.PricingFingerprint(card)
			providerKind := officialKindForBilling(p)
			if entry, ok := pricing.MatchesCatalog(providerKind, p.Model, card); ok {
				info.CatalogMatch = true
				info.CatalogSource = entry.DocURL
			} else if price != nil {
				info.CustomPrice = !info.CatalogMatch
			}
		}
		rep.Providers = append(rep.Providers, info)
	}
	rep.CatalogNotes = []string{
		"User-custom prices always win over the official catalog.",
		"MiMo Token Plan costs are pay-as-you-go equivalents, not plan invoices.",
		"Runtime FX is disabled; only identity and official regional rate-card estimates are used.",
		"Switching display_currency never rewrites provider billing_currency or list prices.",
	}
	return rep
}

// collectCardAudit reads the selected range and copies the counts into the
// report. A read failure keeps Known false and leaves every count nil: the
// report cannot tell, and a zero would claim it found nothing.
func collectCardAudit(opts BillingOptions) *CardAuditReport {
	dir := strings.TrimSpace(opts.StatsDir)
	if dir == "" {
		dir = config.StatsDir()
	}
	from, to := opts.window()
	audit, err := stats.AuditCards(dir, from, to)
	rep := &CardAuditReport{From: audit.From, To: audit.To}
	if err != nil || !audit.Known {
		return rep
	}
	rep.Known = true
	rep.Rows = audit.Rows
	named, unrecorded := audit.Named, audit.Unrecorded
	unresolvable, supersededPeak := audit.Unresolvable, audit.SupersededPeak
	rep.Named = &named
	rep.Unrecorded = &unrecorded
	rep.Unresolvable = &unresolvable
	rep.SupersededPeak = &supersededPeak
	rep.Note = audit.Note
	return rep
}

// window resolves the inclusive range. Explicit days are whole calendar days;
// a trailing window is whole local days so today's morning is not cut off.
func (o BillingOptions) window() (time.Time, time.Time) {
	if !o.From.IsZero() && !o.To.IsZero() {
		from := time.Date(o.From.Year(), o.From.Month(), o.From.Day(), 0, 0, 0, 0, o.From.Location())
		to := time.Date(o.To.Year(), o.To.Month(), o.To.Day(), 23, 59, 59, 0, o.To.Location())
		return from, to
	}
	days := o.Days
	if days <= 0 {
		days = 30
	}
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	to := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, now.Location())
	return to.AddDate(0, 0, -(days - 1)), to
}

func prefLabel(pref string) string {
	if pref == "" {
		return "auto"
	}
	return pref
}

// officialKindForBilling reports which catalog provider's list prices apply to
// p. The endpoint decides: an entry pointing at a proxy resells on its own
// terms, so matching it against the vendor's official rate card would label a
// resold price as official.
func officialKindForBilling(p *config.ProviderEntry) string {
	if p == nil {
		return ""
	}
	return pricing.OfficialProviderForEndpoint(p.BaseURL)
}

// RenderBillingText formats a human-readable billing doctor report.
func RenderBillingText(r BillingReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "reasonix doctor billing\n")
	fmt.Fprintf(&b, "  display preference: %s\n", r.DisplayCurrencyPref)
	fmt.Fprintf(&b, "  display resolved:   %s\n", r.DisplayCurrency)
	fmt.Fprintf(&b, "  fx source:          %s\n", r.FX.Source)
	fmt.Fprintf(&b, "  fx enabled:         %v\n", r.FX.Enabled)
	fmt.Fprintf(&b, "  auto strategy:      wallet currency, then pricing basis\n")
	b.WriteString("\n  providers:\n")
	for _, p := range r.Providers {
		fmt.Fprintf(&b, "    - %s model=%s billing=%s mode=%s", p.Name, p.Model, p.BillingCurrency, p.BillingMode)
		if p.PriceCurrency != "" {
			fmt.Fprintf(&b, " price=%s/%.4g/%.4g", p.PriceCurrency, p.PriceInput, p.PriceOutput)
		}
		if p.CatalogMatch {
			fmt.Fprintf(&b, " [official: %s]", p.CatalogSource)
		} else if p.CustomPrice {
			b.WriteString(" [custom price protected]")
		}
		b.WriteByte('\n')
		if p.Fingerprint != "" {
			fmt.Fprintf(&b, "      fingerprint=%s\n", p.Fingerprint)
		}
	}
	if len(r.CatalogNotes) > 0 {
		b.WriteString("\n  notes:\n")
		for _, n := range r.CatalogNotes {
			fmt.Fprintf(&b, "    - %s\n", n)
		}
	}
	if r.CardAudit != nil {
		b.WriteString("\n  stored rows:\n")
		fmt.Fprintf(&b, "    range: %s..%s\n", r.CardAudit.From, r.CardAudit.To)
		if !r.CardAudit.Known {
			b.WriteString("    cards: unknown (the stored rows could not be read)\n")
		} else {
			fmt.Fprintf(&b, "    rows: %d\n", r.CardAudit.Rows)
			fmt.Fprintf(&b, "    named a card: %s\n", countText(r.CardAudit.Named))
			fmt.Fprintf(&b, "    unresolvable: %s\n", countText(r.CardAudit.Unresolvable))
			fmt.Fprintf(&b, "    no card recorded: %s\n", countText(r.CardAudit.Unrecorded))
			if r.CardAudit.Note != "" {
				fmt.Fprintf(&b, "    note: %s\n", r.CardAudit.Note)
			}
		}
	}
	return b.String()
}

// countText renders a count that may be unknown. A nil pointer is the report
// saying it could not tell, which is not the same as zero.
func countText(n *int) string {
	if n == nil {
		return "unknown"
	}
	return fmt.Sprintf("%d", *n)
}
