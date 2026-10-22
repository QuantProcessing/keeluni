package pairexec

import (
	"errors"
	"testing"
)

func TestTextCarriesCodeParamsAndEnglish(t *testing.T) {
	text := withReason(textOf("failure.hedgeRejected", "leg", 2), venueText("Insufficient margin (-2019)"))
	if text.Code != "failure.hedgeRejected" || text.Params["leg"] != "2" || text.Reason == nil || text.Reason.Code != "" {
		t.Fatalf("text = %+v", text)
	}
	if text.Message != "hedge on leg 2 rejected: Insufficient margin (-2019)" {
		t.Fatalf("message = %q", text.Message)
	}
	joined := joinTexts(textOf("failure.legFilled", "leg", 1, "filled", "0", "target", "0.01"), textOf("failure.legFilled", "leg", 2, "filled", "0.005", "target", "0.01"))
	if joined.Also == nil || joined.Message != "leg 1 filled 0 of 0.01; leg 2 filled 0.005 of 0.01" {
		t.Fatalf("joined = %+v", joined)
	}
}

func TestTextRoundTripsAndReadsOldRows(t *testing.T) {
	text := withReason(textOf("event.broken"), textOf("problem.lateFill"))
	back := decodeText(text.encode())
	if back.Code != text.Code || back.Message != text.Message || back.Reason == nil || back.Reason.Code != "problem.lateFill" {
		t.Fatalf("round trip = %+v", back)
	}
	// Rows written before texts had codes are plain English.
	if old := decodeText("legs unbalanced: leg 1 holds 0.01"); old.Code != "" || old.Message != "legs unbalanced: leg 1 holds 0.01" {
		t.Fatalf("old = %+v", old)
	}
	if !decodeText("").IsZero() || (Text{}).encode() != "" {
		t.Fatal("empty text must stay empty")
	}
}

func TestEveryCodeHasAnEnglishFormat(t *testing.T) {
	for code, format := range textFormats {
		if format == "" {
			t.Errorf("%s has no format", code)
		}
	}
	err := Invalid("invalid.legRoute", "leg", 1, "account", "cx", "inst", "okx:perp:BTC-USDT-SWAP")
	text, ok := InvalidText(err)
	if !ok || !IsInvalid(err) || text.Code != "invalid.legRoute" || err.Error() != "leg 1: account cx cannot trade okx:perp:BTC-USDT-SWAP" {
		t.Fatalf("invalid = %v %+v", err, text)
	}
	if errText(errors.New("boom")).Code != "" {
		t.Fatal("a plain error is shown as its own words")
	}
}
