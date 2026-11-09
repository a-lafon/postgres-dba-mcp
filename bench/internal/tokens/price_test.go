package tokens

import "testing"

func TestInputUSD(t *testing.T) {
	p, ok := PriceOf(DefaultModel)
	if !ok {
		t.Fatalf("no price for the default model %s: every measured run must be priceable", DefaultModel)
	}
	if got := p.InputUSD(1_000_000); got != p.In {
		t.Errorf("a million input tokens cost %v, want %v", got, p.In)
	}
	if got := p.InputUSD(0); got != 0 {
		t.Errorf("no tokens cost %v, want 0", got)
	}
}

func TestEveryPriceIsDated(t *testing.T) {
	for id, p := range Prices {
		if p.Model != id || p.In <= 0 || p.Out <= 0 || p.Date == "" || p.Source == "" {
			t.Errorf("price %q is incomplete: %+v", id, p)
		}
	}
}
