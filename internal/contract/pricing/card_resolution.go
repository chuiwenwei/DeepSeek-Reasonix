package pricing

import "strings"

// CardResolution is what the published history can say about a stored card
// fingerprint. Found is false when no published card matches; SupersededPeak
// then says whether the fingerprint names a superseded generation's peak card,
// which the history does not enumerate. That is a known gap, not a card nobody
// shipped.
type CardResolution struct {
	Card           RateCard
	Found          bool
	SupersededPeak bool
}

// PublishedCards lists the cards this project puts in front of a user for one
// model in one currency: the current off-peak and peak cards plus superseded
// base cards. An empty currency reads every currency the catalog publishes.
// This is the card set a stored fingerprint is resolved against; superseded peak
// cards are deliberately absent, because publishing them is a separate change.
func PublishedCards(provider, model, currency string) []RateCard {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	if provider == "" || model == "" {
		return nil
	}
	currencies := publishedCurrencies(provider, model, currency)
	var cards []RateCard
	for _, cur := range currencies {
		if current := CurrentRate(provider, model, cur); current != nil {
			cards = append(cards, *current)
		}
		for _, entry := range OfficialCatalog() {
			if entry.Provider != provider || entry.Model != model {
				continue
			}
			if NormalizeCurrency(entry.Currency) != cur || entry.Peak == nil {
				continue
			}
			peak := *entry.Peak
			peak.Currency = cur
			cards = append(cards, peak)
		}
		cards = append(cards, SupersededRates(provider, model, cur)...)
	}
	return cards
}

// publishedCurrencies narrows a lookup to one currency, or collects the ones the
// catalog publishes for the model when the caller has none.
func publishedCurrencies(provider, model, currency string) []string {
	if cur := NormalizeCurrency(currency); cur != "" {
		return []string{cur}
	}
	mapped := RateModelFor(provider, model)
	seen := map[string]bool{}
	var out []string
	for _, row := range officialRates {
		if row.Provider != provider || row.Model != mapped {
			continue
		}
		cur := NormalizeCurrency(row.Currency)
		if cur == "" || seen[cur] {
			continue
		}
		seen[cur] = true
		out = append(out, cur)
	}
	return out
}

// ResolveCard is the only matching rule: a card is named when its fingerprint
// equals the stored name. A fingerprint that matches no published card is not
// "today's card" and is not resolved. If it names a superseded peak card,
// SupersededPeak reports the known history gap so a surface can explain the miss
// without implying corruption.
func ResolveCard(provider, model, currency, fingerprint string) CardResolution {
	name := strings.TrimSpace(fingerprint)
	if name == "" {
		return CardResolution{}
	}
	for _, card := range PublishedCards(provider, model, currency) {
		if PricingFingerprint(card) == name {
			return CardResolution{Card: card, Found: true}
		}
	}
	if namesSupersededPeak(provider, model, currency, name) {
		return CardResolution{SupersededPeak: true}
	}
	return CardResolution{}
}

// namesSupersededPeak reports whether fingerprint names the peak card of a
// generation this project has superseded. The card table is read directly
// because the published history does not enumerate these cards; returning a
// card is exactly the change that would close the gap, and this predicate is
// only allowed to explain a miss.
func namesSupersededPeak(provider, model, currency, fingerprint string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = RateModelFor(provider, strings.TrimSpace(model))
	if provider == "" || model == "" {
		return false
	}
	want := NormalizeCurrency(currency)
	for _, row := range officialRates {
		if row.Provider != provider || row.Model != model {
			continue
		}
		if row.BillingMode == BillingModeSubscriptionEquivalent || len(row.Rates) < 2 {
			continue
		}
		cur := NormalizeCurrency(row.Currency)
		if want != "" && cur != want {
			continue
		}
		for _, generation := range row.Rates[:row.currentIndex()] {
			if generation.Peak == nil {
				continue
			}
			peak := *generation.Peak
			peak.Currency = row.Currency
			if PricingFingerprint(peak) == fingerprint {
				return true
			}
		}
	}
	return false
}
