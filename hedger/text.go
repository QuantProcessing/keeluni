package hedger

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Text is a message for the operator, kept as a code and its parameters so
// the interface can show it in the reader's language. Message is the English
// rendering, for logs, for clients that do not know the code, and as the
// fallback when the interface has no translation.
//
// Parameters are plain values (numbers as decimal strings, instruments as
// ids); free text from a venue (a rejection reason) is a parameter too and is
// shown as the venue wrote it.
type Text struct {
	Code    string            `json:"code,omitempty"`
	Params  map[string]string `json:"params,omitempty"`
	Message string            `json:"message"`
	// Reason explains the text ("hedge on leg 2 rejected" because of the
	// venue's "Insufficient margin"); it is a text of its own, coded or a
	// venue's words, and is shown after the text.
	Reason *Text `json:"reason,omitempty"`
	// Also follows the text (two shortfalls of one action).
	Also *Text `json:"also,omitempty"`
}

func (text Text) String() string { return text.Message }

// IsZero reports an empty text.
func (text Text) IsZero() bool { return text.Code == "" && text.Message == "" }

// textFormats is the English template of every code. {name} is replaced by
// the parameter of that name.
var textFormats = map[string]string{
	// Validation (HTTP 400)
	"invalid.unknownInstrument": "unknown instrument {inst}",
	"invalid.noBook":            "no order book for {inst} yet; try again in a moment",
	"invalid.textTooLong":       "label or note is too long",
	"invalid.legUnknownInst":    "leg {leg}: unknown instrument {inst}",
	"invalid.legUnknownAccount": "leg {leg}: unknown account {account}",
	"invalid.legInactive":       "leg {leg}: account {account} is not active",
	"invalid.legMarket":         "leg {leg}: account {account} does not trade {market}",
	"invalid.legRoute":          "leg {leg}: account {account} cannot trade {inst}",
	"invalid.legSide":           "leg {leg}: side must be buy or sell",
	"invalid.legSpotShort":      "leg {leg}: a spot leg can only be long (shorting spot needs margin borrowing)",
	"invalid.legRules":          "leg {leg}: instrument rules are incomplete",
	"invalid.makerLeg":          "maker leg must be 0 or 1",
	"invalid.limitPrice":        "limit mode needs a positive limit price for the maker leg",
	"invalid.tif":               "tif must be gtc or post_only",
	"invalid.mode":              "mode must be market or limit",
	"invalid.sameLegs":          "the two legs are the same instrument on the same account",
	"invalid.sameSide":          "both legs are {side}: a hedge is one long and one short leg",
	"invalid.policy":            "policy must be unwind or hold",
	"invalid.cannotClose":       "a {status} group cannot be closed now",
	"invalid.fraction":          "fraction must be in (0, 1]",
	"invalid.nothingToClose":    "nothing to close at this fraction",
	"invalid.makerNothing":      "the maker leg has nothing to close",
	"invalid.busy":              "the group already has an action working",
	"invalid.cannotRehedge":     "a {status} group cannot be rehedged now",
	"invalid.balanced":          "the legs are balanced",
	"invalid.belowStep":         "the imbalance is below the smaller leg's step",
	"invalid.cannotTrim":        "a {status} group cannot be trimmed now",
	"invalid.nothingToTrim":     "nothing to trim",
	"invalid.cannotUnwind":      "a {status} group cannot be unwound now",
	"invalid.nothingHeld":       "nothing is held",
	"invalid.noWorking":         "no working action to cancel",
	"invalid.sizePositive":      "size must be positive",
	"invalid.noPrice":           "no price for leg {leg} ({inst})",
	"invalid.unit":              "unit must be base or quote",
	"invalid.ratio":             "ratio must be positive",
	"invalid.match":             "match must be base or notional",
	"sizing.roundsToZero":       "leg {leg}: size rounds to zero at step {step}",
	"sizing.belowMinQty":        "leg {leg}: {qty} is below the minimum size {min}",
	"sizing.belowMinNotional":   "leg {leg}: value {value} is below the minimum {min} {quote}",

	// Group problems
	"problem.unbalanced": "legs unbalanced: leg 1 holds {held0}, leg 2 holds {held1} (needs {need})",
	"problem.lateFill":   "a fill arrived after its action finished",
	"problem.venueShort": "leg {leg}: the venue holds {venue} {base} on {inst}, the hedge accounts for {held}; closed outside the hedge, liquidated or auto-deleveraged",

	// Action failures (also used inside problems)
	"failure.legFilled":     "leg {leg} filled {filled} of {target}",
	"failure.notRecorded":   "could not record the order: {error}",
	"failure.unresolved":    "leg {leg} order outcome could not be established",
	"failure.hedgeRejected": "hedge on leg {leg} rejected",
	"failure.legRejected":   "leg {leg} rejected",
	"failure.canceledEarly": "canceled before the maker order was placed",
	"failure.byOperator":    "canceled by the operator",
	"failure.makerEnded":    "maker order {state}",
	"failure.makerStopped":  "maker order {state} after filling {filled} of {target}",

	// Order reasons
	"reason.rejected":       "rejected by the exchange",
	"reason.outcomeUnknown": "outcome unknown",
	"reason.unresolved":     "the exchange can neither show nor deny this order and no fill of it was found; check the venue before acting",
	"reason.notFound":       "the exchange has no such order; it was never placed",

	// Events (the activity log)
	"event.opening":         "Opening: {side0} {qty0} {inst0} / {side1} {qty1} {inst1} (ratio {ratio}, {mode} mode)",
	"event.closing":         "Closing {pct}%: leg 1 {qty0}, leg 2 {qty1} ({mode} mode)",
	"event.unwinding":       "Unwinding: leg 1 {qty0}, leg 2 {qty1} at market",
	"event.rehedging":       "Rehedging",
	"event.trimming":        "Trimming to the matched size",
	"event.target":          "leg {leg} {side} {qty}",
	"event.late":            "Late venue update: status {from} → {to}",
	"event.broken":          "Hedge broken",
	"event.repairSkipped":   "Automatic repair skipped: an order's outcome is unknown; check the venue position, then Rehedge, Trim or Unwind",
	"event.trimNotStarted":  "Automatic trim not started",
	"event.unhedged":        "Unhedged. Check the venue, then Rehedge, Trim or Unwind",
	"event.riskOrder":       "{origin} order on leg {leg} ({inst}): {side} {qty}, status {status}",
	"event.cancelRequested": "Cancel requested",
	"event.resumed":         "Resumed {kind} after restart; reconciling orders with the venues",
	"event.makerFilled":     "Maker leg {leg} filled {filled} of {target}",
	"event.residual":        "Residual {qty} on leg {leg} is below its minimum tradable size and stays unhedged",
	"event.notSent":         "Order for leg {leg} not sent: the database refused it ({error})",
	"event.sending":         "{role} {type} {side} {qty} on leg {leg} {inst} (client {client})",
	"event.sendingAt":       "{role} {type} {side} {qty} on leg {leg} {inst} @ {price} (client {client})",
	"event.cancelFailed":    "Cancel of the maker order failed ({error}); checking its state",
	"event.unknown":         "Leg {leg} order outcome unknown; checking with the exchange, it will not be placed again",
	"event.orderState":      "Leg {leg} {side} order {state}",
	"event.orderRejected":   "Leg {leg} {side} order rejected",
	"event.progress":        "Leg {leg} {side} {state}: {filled}/{qty} filled @ {price}",
	"event.progressNoPrice": "Leg {leg} {side} {state}: {filled}/{qty} filled",
	"event.done":            "{kind} done",
	"event.canceled":        "{kind} canceled",
	"event.failed":          "{kind} failed",
}

// textOf builds a Text from a code and name/value pairs.
func textOf(code string, pairs ...any) Text {
	params := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		params[fmt.Sprint(pairs[i])] = fmt.Sprint(pairs[i+1])
	}
	format, ok := textFormats[code]
	if !ok {
		format = code
	}
	message := format
	for name, value := range params {
		message = strings.ReplaceAll(message, "{"+name+"}", value)
	}
	if len(params) == 0 {
		params = nil
	}
	return Text{Code: code, Params: params, Message: message}
}

// encode stores a text in a text column: a JSON object, so older rows (plain
// English) and newer ones can be told apart.
func (text Text) encode() string {
	if text.IsZero() {
		return ""
	}
	data, _ := json.Marshal(text)
	return string(data)
}

// decodeText reads a stored text; a plain string (rows written before texts
// had codes) becomes a text with only its English message.
func decodeText(stored string) Text {
	if stored == "" {
		return Text{}
	}
	if strings.HasPrefix(stored, "{") {
		var text Text
		if json.Unmarshal([]byte(stored), &text) == nil && text.Message != "" {
			return text
		}
	}
	return Text{Message: stored}
}

// venueText is a venue's or the gateway's own words (a rejection reason): it
// has no code and is shown as written in every language.
func venueText(message string) Text {
	return Text{Message: message}
}

// withReason attaches why to text; its English message becomes
// "text: why". An empty reason leaves text as it is.
func withReason(text, reason Text) Text {
	if reason.IsZero() {
		return text
	}
	r := reason
	text.Reason = &r
	text.Message += ": " + reason.Message
	return text
}

// joinTexts lists b after a ("a; b").
func joinTexts(a, b Text) Text {
	tail := b
	a.Also = &tail
	a.Message += "; " + b.Message
	return a
}

// errText is an error as a text: a refused request keeps its code, anything
// else (a gateway or database failure) is shown as its own words.
func errText(err error) Text {
	if text, ok := invalidText(err); ok {
		return text
	}
	return venueText(errorText(err))
}
