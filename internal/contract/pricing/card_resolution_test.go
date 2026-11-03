package pricing

import "testing"

// The published card history has one known hole: it lists the base card of every
// superseded generation but not that generation's peak card. A row priced on one
// therefore names a card the history cannot resolve. ResolveCard reports that as
// a known gap and still does not resolve it: closing the gap is a change to the
// published history, not to this matcher.
func TestResolveCardNamesTheSupersededPeakGapWithoutClosingIt(t *testing.T) {
	const (
		provider = "deepseek"
		model    = "deepseek-flash"
		currency = "CNY"
	)
	currentBase := RateCard{CacheHit: 0.02, Input: 1, Output: 4, Currency: currency}
	currentPeak := RateCard{CacheHit: 0.04, Input: 2, Output: 8, Currency: currency}
	supersededBase := RateCard{CacheHit: 0.05, Input: 1.5, Output: 4.5, Currency: currency}
	supersededPeak := RateCard{CacheHit: 0.10, Input: 3.0, Output: 9.0, Currency: currency}

	for _, tc := range []struct {
		name string
		card RateCard
		want CardResolution
	}{
		{"current base resolves", currentBase, CardResolution{Card: currentBase, Found: true}},
		{"current peak resolves", currentPeak, CardResolution{Card: currentPeak, Found: true}},
		{"superseded base resolves", supersededBase, CardResolution{Card: supersededBase, Found: true}},
		{"superseded peak is the known gap", supersededPeak, CardResolution{SupersededPeak: true}},
		{"a card nobody shipped resolves to nothing", RateCard{CacheHit: 1, Input: 2, Output: 3, Currency: currency}, CardResolution{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveCard(provider, model, currency, PricingFingerprint(tc.card))
			if got.Found != tc.want.Found || got.SupersededPeak != tc.want.SupersededPeak {
				t.Fatalf("ResolveCard(%+v) = found=%v supersededPeak=%v, want found=%v supersededPeak=%v",
					tc.card, got.Found, got.SupersededPeak, tc.want.Found, tc.want.SupersededPeak)
			}
			if got.Found && got.Card != tc.want.Card {
				t.Fatalf("ResolveCard(%+v) = %+v, want %+v", tc.card, got.Card, tc.want.Card)
			}
		})
	}

	// The resolver must not publish the gap away: if a superseded peak became a
	// published card, Found would turn true and the follow-up would look done.
	for _, card := range PublishedCards(provider, model, currency) {
		if sameCard(card, supersededPeak) {
			t.Fatalf("PublishedCards lists the superseded peak %+v; the published history still has the gap", card)
		}
	}
}

// A row may predate cost_currency, so an empty currency has to search every
// currency the catalog publishes for the model rather than fail the lookup.
func TestResolveCardWithoutACurrencySearchesEveryPublishedCurrency(t *testing.T) {
	usd := RateCard{CacheHit: 0.003, Input: 0.15, Output: 0.6, Currency: "USD"}
	got := ResolveCard("deepseek", "deepseek-flash", "", PricingFingerprint(usd))
	if !got.Found || got.Card != usd {
		t.Fatalf("ResolveCard with no currency = %+v, want the USD card %+v", got, usd)
	}
}

func TestResolveCardReadsAnAbsentNameAsNothing(t *testing.T) {
	if got := ResolveCard("deepseek", "deepseek-flash", "CNY", "  "); got.Found || got.SupersededPeak {
		t.Fatalf("an empty fingerprint resolved to %+v", got)
	}
}
