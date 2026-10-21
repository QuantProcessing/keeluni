package exchange

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func optionInstrument() Instrument {
	return Instrument{
		Identity: InstrumentIdentity{Venue: VenueDeribit, Product: ProductOption, Symbol: "BTC-USD-20261127-73000-P", BaseAsset: "BTC", QuoteAsset: "USD",
			SettleAsset: "BTC", NativeSymbol: "BTC-27NOV26-73000-P"},
		Rules: InstrumentRules{PriceIncrement: decimal.RequireFromString("0.0001"), QuantityIncrement: decimal.RequireFromString("0.1"), MinQuantity: decimal.RequireFromString("0.1"),
			CoarseTickFrom: OptionalDecimal{Value: decimal.RequireFromString("0.005"), Valid: true}, CoarseTick: OptionalDecimal{Value: decimal.RequireFromString("0.0005"), Valid: true}},
		Metadata: InstrumentMetadata{MarketFamily: MarketFamilyOption, AssetClass: AssetClassCrypto},
		Option: OptionTerms{Underlying: "BTC", Kind: OptionPut, Strike: decimal.NewFromInt(73000), Expiry: time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC),
			PremiumAsset: "BTC", Inverse: true, Index: "btc_usd", European: true, CashSettled: true},
	}
}

func TestOptionProductIsModelled(t *testing.T) {
	if !KnownProduct(ProductOption) || !KnownProduct(ProductSpot) || !KnownProduct(ProductPerp) || KnownProduct("future") {
		t.Fatal("known products")
	}
	if !IsDerivative(ProductOption) || !IsDerivative(ProductPerp) || IsDerivative(ProductSpot) {
		t.Fatal("derivative products")
	}
	option := optionInstrument()
	if err := option.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := option.EffectiveMetadata().MarketFamily; got != MarketFamilyOption {
		t.Fatalf("family = %s", got)
	}
	broken := option
	broken.Option.Kind = "straddle"
	if broken.Validate() == nil {
		t.Fatal("unknown option kind accepted")
	}
	broken = option
	broken.Option = OptionTerms{}
	if !broken.Option.IsZero() || broken.Validate() == nil {
		t.Fatal("option without terms accepted")
	}
	perp := option
	perp.Identity.Product, perp.Metadata.MarketFamily = ProductPerp, MarketFamilyVenuePerpetual
	if perp.Validate() == nil {
		t.Fatal("a perp carrying option terms accepted")
	}
	for _, kind := range []OptionKind{OptionCall, OptionPut} {
		terms := option.Option
		terms.Kind = kind
		if terms.Validate() != nil {
			t.Fatal(kind)
		}
	}
}

func TestCoarseTickAppliesAboveItsBoundary(t *testing.T) {
	option := optionInstrument()
	rules := option.Rules
	if !rules.TickAt(decimal.RequireFromString("0.0049")).Equal(decimal.RequireFromString("0.0001")) || !rules.TickAt(decimal.RequireFromString("0.005")).Equal(decimal.RequireFromString("0.0005")) {
		t.Fatal("tick at price")
	}
	order := PlaceOrderRequest{Instrument: option, ClientOrderID: "5", Side: SideSell, Type: OrderTypeLimit, Quantity: decimal.RequireFromString("0.5"),
		LimitPrice: decimal.RequireFromString("0.0123"), LimitPolicy: LimitPolicyResting, ReduceOnly: true}
	if err := order.Validate(ProductOption); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("0.0123 is off the 0.0005 tick: %v", err)
	}
	order.LimitPrice = decimal.RequireFromString("0.0125")
	if err := order.Validate(ProductOption); err != nil {
		t.Fatalf("reduce-only option order: %v", err)
	}
	incomplete := option
	incomplete.Rules.CoarseTick = OptionalDecimal{}
	if incomplete.Validate() == nil {
		t.Fatal("half a coarse tick accepted")
	}
}

type optionClientShape struct{}

func (optionClientShape) OptionChain(context.Context, OptionChainRequest) ([]OptionQuote, error) {
	return []OptionQuote{{Instrument: optionInstrument(), MarkIV: OptionalDecimal{Value: decimal.RequireFromString("0.4039"), Valid: true},
		Greeks: &OptionGreeks{Delta: decimal.RequireFromString("-0.169")}}}, nil
}
func (optionClientShape) Balances(context.Context) ([]Balance, error) { return nil, nil }
func (optionClientShape) OptionAccount(context.Context) (OptionAccount, error) {
	return OptionAccount{SettleAsset: "USDC"}, nil
}
func (optionClientShape) Positions(context.Context, PositionsRequest) ([]Position, error) {
	return nil, nil
}
func (optionClientShape) WatchOptionQuote(context.Context, WatchRequest) (Subscription[OptionQuote], error) {
	return nil, nil
}

func TestOptionInterfacesShape(t *testing.T) {
	var shape optionClientShape
	var market OptionMarketREST = shape
	var account OptionAccountREST = shape
	var quotes OptionQuoteWebSocket = shape
	chain, _ := market.OptionChain(context.Background(), OptionChainRequest{Underlying: "BTC"})
	if len(chain) != 1 || chain[0].Instrument.Option.Kind != OptionPut || chain[0].Greeks == nil {
		t.Fatalf("chain = %+v", chain)
	}
	if got, _ := account.OptionAccount(context.Background()); got.SettleAsset != "USDC" {
		t.Fatal("account")
	}
	_, _ = account.Balances(context.Background())
	_, _ = account.Positions(context.Background(), PositionsRequest{})
	_, _ = quotes.WatchOptionQuote(context.Background(), WatchRequest{})
	var _ OptionClient
	var _ OptionWebSocket
	if NativeMarketFamily(VenueDeribit, ProductOption, "") != NativeFamilyOption || len(MarketCapabilitiesFor(VenueDeribit, NativeFamilyOption)) == 0 {
		t.Fatal("deribit options have no market support")
	}
}
