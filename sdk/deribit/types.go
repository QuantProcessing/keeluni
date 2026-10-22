package deribit

import (
	"encoding/json"
	"strings"
)

type AuthResult struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
}

type Instrument struct {
	InstrumentName     string      `json:"instrument_name"`
	Kind               string      `json:"kind"`
	InstrumentType     string      `json:"instrument_type"` // linear, reversed
	SettlementPeriod   string      `json:"settlement_period"`
	BaseCurrency       string      `json:"base_currency"`
	QuoteCurrency      string      `json:"quote_currency"`
	CounterCurrency    string      `json:"counter_currency"`
	SettlementCurrency string      `json:"settlement_currency"`
	TickSize           json.Number `json:"tick_size"`
	ContractSize       json.Number `json:"contract_size"`
	MinTradeAmount     json.Number `json:"min_trade_amount"`
	MaxLeverage        json.Number `json:"max_leverage"`
	MakerCommission    json.Number `json:"maker_commission"`
	TakerCommission    json.Number `json:"taker_commission"`
	IsActive           bool        `json:"is_active"`
	State              string      `json:"state"`
	UnderlyingType     string      `json:"underlying_type"`
	ProductGroup       string      `json:"product_group"`
	TickSizeSteps      []struct {
		AbovePrice json.Number `json:"above_price"`
		TickSize   json.Number `json:"tick_size"`
	} `json:"tick_size_steps"`
	// Options only.
	Strike              json.Number `json:"strike"`
	OptionType          string      `json:"option_type"` // call, put
	ExpirationTimestamp int64       `json:"expiration_timestamp"`
	PriceIndex          string      `json:"price_index"`
	// Spot only: orders routed to an external venue cannot be listed by
	// public trades (Coinbase-routed spot).
	IsCBERouted bool `json:"is_cbe_routed"`
}

// Option reports an active listed option.
func (i Instrument) Option() bool { return i.Kind == "option" && i.IsActive }

// Spot reports an open spot pair.
func (i Instrument) Spot() bool { return i.Kind == "spot" && i.IsActive && i.State == "open" }

// Greeks are a venue's per-contract (ticker) or per-position greeks.
type Greeks struct {
	Delta json.Number `json:"delta"`
	Gamma json.Number `json:"gamma"`
	Vega  json.Number `json:"vega"`
	Theta json.Number `json:"theta"`
	Rho   json.Number `json:"rho"`
}

// LinearPerpetual reports a USDC-settled linear perpetual.
func (i Instrument) LinearPerpetual() bool {
	return i.Kind == "future" && i.SettlementPeriod == "perpetual" && i.InstrumentType == "linear" &&
		strings.HasSuffix(i.InstrumentName, "-PERPETUAL") && i.IsActive
}

type Stats struct {
	High        json.Number `json:"high"`
	Low         json.Number `json:"low"`
	PriceChange json.Number `json:"price_change"`
	Volume      json.Number `json:"volume"`
}

type Ticker struct {
	InstrumentName string      `json:"instrument_name"`
	Timestamp      int64       `json:"timestamp"`
	State          string      `json:"state"`
	LastPrice      json.Number `json:"last_price"`
	MarkPrice      json.Number `json:"mark_price"`
	IndexPrice     json.Number `json:"index_price"`
	BestBidPrice   json.Number `json:"best_bid_price"`
	BestBidAmount  json.Number `json:"best_bid_amount"`
	BestAskPrice   json.Number `json:"best_ask_price"`
	BestAskAmount  json.Number `json:"best_ask_amount"`
	OpenInterest   json.Number `json:"open_interest"`
	CurrentFunding json.Number `json:"current_funding"`
	Funding8h      json.Number `json:"funding_8h"`
	InterestValue  json.Number `json:"interest_value"`
	Stats          Stats       `json:"stats"`
	// Options only. IVs are percent (40.39 is 40.39%).
	Greeks          *Greeks     `json:"greeks"`
	MarkIV          json.Number `json:"mark_iv"`
	BidIV           json.Number `json:"bid_iv"`
	AskIV           json.Number `json:"ask_iv"`
	UnderlyingPrice json.Number `json:"underlying_price"`
	UnderlyingIndex string      `json:"underlying_index"`
}

// BookSummary is one row of get_book_summary_by_currency.
type BookSummary struct {
	InstrumentName string      `json:"instrument_name"`
	BaseCurrency   string      `json:"base_currency"`
	MarkPrice      json.Number `json:"mark_price"`
	OpenInterest   json.Number `json:"open_interest"`
	CurrentFunding json.Number `json:"current_funding"`
	Funding8h      json.Number `json:"funding_8h"`
	Volume         json.Number `json:"volume"`
	High           json.Number `json:"high"`
	Low            json.Number `json:"low"`
	Last           json.Number `json:"last"`
	PriceChange    json.Number `json:"price_change"`
	// VolumeUSD is the 24h volume in USD (futures and perpetuals).
	VolumeUSD json.Number `json:"volume_usd"`
	// Options and spot.
	BidPrice          json.Number `json:"bid_price"`
	AskPrice          json.Number `json:"ask_price"`
	MarkIV            json.Number `json:"mark_iv"`
	UnderlyingPrice   json.Number `json:"underlying_price"`
	UnderlyingIndex   string      `json:"underlying_index"`
	EstimatedDelivery json.Number `json:"estimated_delivery_price"`
	CreationTimestamp int64       `json:"creation_timestamp"`
}

// OrderBook levels are [price, amount] for REST snapshots.
type OrderBook struct {
	InstrumentName string           `json:"instrument_name"`
	Timestamp      int64            `json:"timestamp"`
	ChangeID       int64            `json:"change_id"`
	Bids           [][2]json.Number `json:"bids"`
	Asks           [][2]json.Number `json:"asks"`
	MarkPrice      json.Number      `json:"mark_price"`
	LastPrice      json.Number      `json:"last_price"`
	Stats          Stats            `json:"stats"`
	CurrentFunding json.Number      `json:"current_funding"`
	Funding8h      json.Number      `json:"funding_8h"`
}

// Trade is a public or user trade. Liquidity (M, T) and Fee are set on user
// trades only.
type Trade struct {
	IV             json.Number     `json:"iv"`
	IndexPrice     json.Number     `json:"index_price"`
	TradeID        string          `json:"trade_id"`
	TradeSeq       int64           `json:"trade_seq"`
	InstrumentName string          `json:"instrument_name"`
	Timestamp      int64           `json:"timestamp"`
	Direction      string          `json:"direction"`
	Price          json.Number     `json:"price"`
	Amount         json.Number     `json:"amount"`
	OrderID        string          `json:"order_id"`
	Label          string          `json:"label"`
	Liquidity      string          `json:"liquidity"`
	Fee            json.Number     `json:"fee"`
	FeeCurrency    string          `json:"fee_currency"`
	State          string          `json:"state"`
	ReduceOnly     json.RawMessage `json:"reduce_only"`
}

type Chart struct {
	Status string        `json:"status"`
	Ticks  []int64       `json:"ticks"`
	Open   []json.Number `json:"open"`
	High   []json.Number `json:"high"`
	Low    []json.Number `json:"low"`
	Close  []json.Number `json:"close"`
	Volume []json.Number `json:"volume"`
}

type FundingPoint struct {
	Timestamp  int64       `json:"timestamp"`
	Interest1h json.Number `json:"interest_1h"`
	Interest8h json.Number `json:"interest_8h"`
	IndexPrice json.Number `json:"index_price"`
}

type AccountSummary struct {
	Currency          string      `json:"currency"`
	Balance           json.Number `json:"balance"`
	Equity            json.Number `json:"equity"`
	MarginBalance     json.Number `json:"margin_balance"`
	AvailableFunds    json.Number `json:"available_funds"`
	InitialMargin     json.Number `json:"initial_margin"`
	MaintenanceMargin json.Number `json:"maintenance_margin"`
	FuturesPL         json.Number `json:"futures_pl"`
	SessionUPL        json.Number `json:"session_upl"`
	TotalPL           json.Number `json:"total_pl"`
	// Options figures, in the summary's currency.
	OptionsValue json.Number `json:"options_value"`
	OptionsPL    json.Number `json:"options_pl"`
	OptionsDelta json.Number `json:"options_delta"`
	OptionsGamma json.Number `json:"options_gamma"`
	OptionsVega  json.Number `json:"options_vega"`
	OptionsTheta json.Number `json:"options_theta"`
	DeltaTotal   json.Number `json:"delta_total"`
	MarginModel  string      `json:"margin_model"`
}

// Position.Size is signed in base units for linear instruments (negative is
// short); Direction is buy, sell or zero.
type Position struct {
	InstrumentName            string      `json:"instrument_name"`
	Kind                      string      `json:"kind"`
	Direction                 string      `json:"direction"`
	Size                      json.Number `json:"size"`
	AveragePrice              json.Number `json:"average_price"`
	MarkPrice                 json.Number `json:"mark_price"`
	FloatingProfitLoss        json.Number `json:"floating_profit_loss"`
	EstimatedLiquidationPrice json.Number `json:"estimated_liquidation_price"`
	Leverage                  json.Number `json:"leverage"`
	InitialMargin             json.Number `json:"initial_margin"`
	MaintenanceMargin         json.Number `json:"maintenance_margin"`
	// Options: position greeks, in the settlement currency of the option
	// for inverse options (BTC, ETH) and in USDC for linear ones.
	Delta                 json.Number `json:"delta"`
	Gamma                 json.Number `json:"gamma"`
	Vega                  json.Number `json:"vega"`
	Theta                 json.Number `json:"theta"`
	IndexPrice            json.Number `json:"index_price"`
	AveragePriceUSD       json.Number `json:"average_price_usd"`
	FloatingProfitLossUSD json.Number `json:"floating_profit_loss_usd"`
}

// Order.Price is a number, or "market_price" for market orders.
type Order struct {
	OrderID             string          `json:"order_id"`
	Label               string          `json:"label"`
	InstrumentName      string          `json:"instrument_name"`
	Direction           string          `json:"direction"`
	OrderType           string          `json:"order_type"`
	OrderState          string          `json:"order_state"` // open, filled, rejected, cancelled, untriggered
	TimeInForce         string          `json:"time_in_force"`
	Price               json.RawMessage `json:"price"`
	Amount              json.Number     `json:"amount"`
	FilledAmount        json.Number     `json:"filled_amount"`
	AveragePrice        json.Number     `json:"average_price"`
	PostOnly            bool            `json:"post_only"`
	ReduceOnly          bool            `json:"reduce_only"`
	IsLiquidation       bool            `json:"is_liquidation"`
	CancelReason        string          `json:"cancel_reason"`
	CreationTimestamp   int64           `json:"creation_timestamp"`
	LastUpdateTimestamp int64           `json:"last_update_timestamp"`
	// Trigger orders (stop_market, take_market, ...): TriggerPrice compared
	// with Trigger (last_price | mark_price | index_price); Triggered once
	// fired.
	TriggerPrice json.Number `json:"trigger_price"`
	Trigger      string      `json:"trigger"`
	Triggered    bool        `json:"triggered"`
}

// LimitPrice returns the numeric price, or "" for market_price.
func (o Order) LimitPrice() string {
	text := strings.Trim(strings.TrimSpace(string(o.Price)), `"`)
	if text == "" || text == "market_price" || text == "null" {
		return ""
	}
	return text
}

// OrderRequest is private/buy or private/sell. Type is limit or market;
// TimeInForce good_til_cancelled, immediate_or_cancel or fill_or_kill.
type OrderRequest struct {
	Direction   string
	Instrument  string
	Amount      string
	Type        string
	Price       string
	TimeInForce string
	Label       string
	PostOnly    bool
	ReduceOnly  bool
	// TriggerPrice and Trigger make a stop_market / take_market order.
	TriggerPrice string
	Trigger      string
}

type PlaceResult struct {
	Order  Order   `json:"order"`
	Trades []Trade `json:"trades"`
}
