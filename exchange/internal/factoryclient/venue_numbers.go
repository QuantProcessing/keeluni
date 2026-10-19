package factoryclient

import (
	"encoding/json"
	"strings"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/shopspring/decimal"
)

// Number helpers for SDKs that decode numerics as json.Number (Kraken,
// Deribit). A missing or malformed value is zero; venues send null for
// absent fields.

type jsonNumber = json.Number

func numberDecimal(value json.Number) decimal.Decimal { return stringDecimal(string(value)) }

func stringDecimal(value string) decimal.Decimal {
	parsed, err := decimal.NewFromString(strings.TrimSpace(value))
	if err != nil {
		return decimal.Zero
	}
	return parsed
}

// numberOptional is valid whenever the venue sent a number.
func numberOptional(value json.Number) exchange.OptionalDecimal {
	parsed, err := decimal.NewFromString(strings.TrimSpace(string(value)))
	if err != nil {
		return exchange.OptionalDecimal{}
	}
	return exchange.OptionalDecimal{Value: parsed, Valid: true}
}
