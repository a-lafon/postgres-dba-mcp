package tokens

import "fmt"

// Price is what one model costs per million tokens. The bench turns token counts into dollars with it, and every
// dollar figure it prints names the model, the price and the date, because a price is only true on a date.
//
// To update: change the two numbers of a row and its Date and Source. Nothing else reads a price.
type Price struct {
	Model  string
	In     float64 // USD per million input tokens
	Out    float64 // USD per million output tokens
	Date   string  // when these prices were read, YYYY-MM-DD
	Source string
}

// pricesSource is where the rows below come from.
const pricesSource = "platform.claude.com/docs pricing table, as bundled with the claude-api skill"

// Prices holds the models the bench can count against, by model id.
var Prices = map[string]Price{
	"claude-sonnet-5-5": {Model: "claude-sonnet-5-5", In: 2.00, Out: 10.00, Date: "2026-10-06", Source: pricesSource},
	"claude-opus-5-5":   {Model: "claude-opus-5-5", In: 4.00, Out: 20.00, Date: "2026-10-06", Source: pricesSource},
	"claude-haiku-5-5":  {Model: "claude-haiku-5-5", In: 0.10, Out: 0.50, Date: "2026-10-06", Source: pricesSource},
}

// PriceOf returns the price of a model, and whether the table knows it.
func PriceOf(model string) (Price, bool) {
	p, ok := Prices[model]
	return p, ok
}

// InputUSD is what n input tokens cost at this price. Every number the bench measures is input: tool definitions and
// tool responses are what the server puts into the model's context, never what the model writes.
func (p Price) InputUSD(n int) float64 { return float64(n) * p.In / 1e6 }

// Label says at what price and on what date a dollar figure was computed.
func (p Price) Label() string {
	return fmt.Sprintf("%s input at $%.2f/Mtok, priced %s (%s)", p.Model, p.In, p.Date, p.Source)
}
