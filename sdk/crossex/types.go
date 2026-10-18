// Package crossex is a faithful client for Gate CrossEx, the cross-exchange
// trading account that routes orders to Binance, OKX, Gate, Bybit, Kraken,
// Hyperliquid, Deribit and Lighter from one margin pool.
//
// Specification: Gate CrossEx API v1.0.6 and CrossEx WebSocket v1.0.0
// (https://www.gate.com/docs/developers/crossex/). Every numeric field is a
// decimal string on the wire and stays a string here; normalization belongs to
// the exchange layer.
package crossex

import (
	"encoding/json"
	"strings"
)

// Order states. NEW means CrossEx accepted and validated the order but has not
// yet confirmed it on the target exchange; it is not resting.
const (
	StateNew             = "NEW"
	StateOpen            = "OPEN"
	StatePartiallyFilled = "PARTIALLY_FILLED"
	StateFilled          = "FILLED"
	StateFail            = "FAIL"   // rejected by CrossEx; Reason is JSON {label,message}
	StateReject          = "REJECT" // rejected by the target exchange; Reason is its raw text
	StateCancelled       = "CANCELLED"
)

// Order attributes. Anything other than COMMON was not placed by the user.
const (
	AttributeCommon     = "COMMON"
	AttributeLiq        = "LIQ"        // liquidation takeover
	AttributeReduce     = "REDUCE"     // liquidation reduction
	AttributeADL        = "ADL"        // auto-deleveraging
	AttributeSettlement = "SETTLEMENT" // delisting settlement
)

const (
	AccountModeCross    = "CROSS_EXCHANGE"
	AccountModeIsolated = "ISOLATED_EXCHANGE"

	PositionModeSingle = "SINGLE"
	PositionModeDual   = "DUAL"

	BusinessSpot   = "SPOT"
	BusinessFuture = "FUTURE"
	BusinessMargin = "MARGIN"
)

// Symbol is one row of GET /crossex/rule/symbols.
type Symbol struct {
	Symbol          string  `json:"symbol"`        // EXCHANGE_BUSINESS_BASE_QUOTE
	ExchangeType    string  `json:"exchange_type"` // BINANCE, OKX, ..., or US/HK/KR/JP for securities
	BusinessType    string  `json:"business_type"` // SPOT, FUTURE, MARGIN, STOCK, ETF
	State           string  `json:"state"`         // live, suspend
	MinSize         string  `json:"min_size"`
	MinNotional     *string `json:"min_notional"`
	LotSize         string  `json:"lot_size"`
	TickSize        string  `json:"tick_size"`
	MaxNumOrders    string  `json:"max_num_orders"`
	MaxMarketSize   *string `json:"max_market_size"`
	MaxLimitSize    *string `json:"max_limit_size"`
	ContractSize    *string `json:"contract_size"`
	LiquidationFee  *string `json:"liquidation_fee"`
	DefaultLeverage *string `json:"default_leverage"`
	DelistTime      string  `json:"delist_time"`
	SupportRPI      string  `json:"support_rpi"`
	SupportCross    string  `json:"support_cross"`
	Tag             string  `json:"tag"` // Hyperliquid: builder dex name; securities: description
}

// Base and Quote split the symbol's trailing BASE_QUOTE. Base may contain
// lowercase letters (Hyperliquid kPEPE) but never an underscore.
func (s Symbol) Base() string {
	parts := strings.Split(s.Symbol, "_")
	if len(parts) != 4 {
		return ""
	}
	return parts[2]
}

func (s Symbol) Quote() string {
	parts := strings.Split(s.Symbol, "_")
	if len(parts) != 4 {
		return ""
	}
	return parts[3]
}

// Account is GET /crossex/accounts.
type Account struct {
	UserID                string         `json:"user_id"`
	AvailableMargin       string         `json:"available_margin"`
	MarginBalance         string         `json:"margin_balance"`
	InitialMargin         string         `json:"initial_margin"`
	MaintenanceMargin     string         `json:"maintenance_margin"`
	InitialMarginRate     string         `json:"initial_margin_rate"`
	MaintenanceMarginRate string         `json:"maintenance_margin_rate"`
	PositionMode          string         `json:"position_mode"`
	AccountLimit          string         `json:"account_limit"`
	CreateTime            string         `json:"create_time"`
	UpdateTime            string         `json:"update_time"`
	AccountMode           string         `json:"account_mode"`
	ExchangeType          string         `json:"exchange_type"`
	Assets                []AccountAsset `json:"assets"`
}

// AccountAsset is one (exchange, coin) row of the account, also pushed on the
// "asset" channel.
type AccountAsset struct {
	UserID                     string `json:"user_id"`
	Coin                       string `json:"coin"`
	ExchangeType               string `json:"exchange_type"`
	Price                      string `json:"price,omitempty"` // push only
	Balance                    string `json:"balance"`
	Upnl                       string `json:"upnl"`
	Equity                     string `json:"equity"`
	FuturesInitialMargin       string `json:"futures_initial_margin"`
	FuturesMaintenanceMargin   string `json:"futures_maintenance_margin"`
	BorrowingInitialMargin     string `json:"borrowing_initial_margin"`
	BorrowingMaintenanceMargin string `json:"borrowing_maintenance_margin"`
	AvailableBalance           string `json:"available_balance"`
	Liability                  string `json:"liability"`
}

// Position is one futures position, from GET /crossex/positions or the
// "position" channel.
type Position struct {
	UserID            string `json:"user_id"`
	PositionID        string `json:"position_id"`
	Symbol            string `json:"symbol"`
	PositionSide      string `json:"position_side"` // NONE (single mode), LONG, SHORT
	InitialMargin     string `json:"initial_margin"`
	IsolatedMargin    string `json:"isolated_margin"`
	MarginMode        string `json:"margin_mode"` // CROSS, ISOLATED
	MaintenanceMargin string `json:"maintenance_margin"`
	PositionQty       string `json:"position_qty"` // signed in single mode
	PositionValue     string `json:"position_value"`
	Upnl              string `json:"upnl"`
	UpnlRate          string `json:"upnl_rate"`
	EntryPrice        string `json:"entry_price"`
	LiqPrice          string `json:"liq_price"` // 0 in cross margin
	MarkPrice         string `json:"mark_price"`
	Leverage          string `json:"leverage"`
	MaxLeverage       string `json:"max_leverage"`
	RiskLimit         string `json:"risk_limit"`
	Fee               string `json:"fee"`
	FundingFee        string `json:"funding_fee"`
	FundingTime       string `json:"funding_time"`
	CreateTime        string `json:"create_time"`
	UpdateTime        string `json:"update_time"`
	ClosedPnl         string `json:"closed_pnl"`
}

// Order is one order, from order queries or the "order" channel.
type Order struct {
	UserID             string `json:"user_id"`
	OrderID            string `json:"order_id"`
	Text               string `json:"text"`
	State              string `json:"state"`
	Symbol             string `json:"symbol"`
	Side               string `json:"side"`
	Type               string `json:"type"`
	Attribute          string `json:"attribute"`
	ExchangeType       string `json:"exchange_type"`
	BusinessType       string `json:"business_type"`
	Qty                string `json:"qty"`
	QuoteQty           string `json:"quote_qty"`
	Price              string `json:"price"`
	TimeInForce        string `json:"time_in_force"`
	ExecutedQty        string `json:"executed_qty"`
	ExecutedAmount     string `json:"executed_amount"`
	ExecutedAvgPrice   string `json:"executed_avg_price"`
	FeeCoin            string `json:"fee_coin"`
	Fee                string `json:"fee"`
	ReduceOnly         string `json:"reduce_only"` // "true" / "false"
	Leverage           string `json:"leverage"`
	Reason             string `json:"reason"`
	LastExecutedQty    string `json:"last_executed_qty"`
	LastExecutedPrice  string `json:"last_executed_price"`
	LastExecutedAmount string `json:"last_executed_amount"`
	PositionSide       string `json:"position_side"`
	CreateTime         string `json:"create_time"`
	UpdateTime         string `json:"update_time"`
}

// UnmarshalJSON accepts the client order id as either "text" (documented
// field) or "client_order_id" (used by the open_orders example).
func (o *Order) UnmarshalJSON(data []byte) error {
	type plain Order
	var wire struct {
		plain
		ClientOrderID string `json:"client_order_id"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*o = Order(wire.plain)
	if o.Text == "" {
		o.Text = wire.ClientOrderID
	}
	return nil
}

// RejectReason is the structured reason of a FAIL order.
type RejectReason struct {
	Label   string `json:"label"`
	Message string `json:"message"`
}

// FailReason parses Reason of a FAIL order. ok is false when the reason is not
// the documented JSON object; REJECT reasons are exchange text and never parse.
func (o Order) FailReason() (reason RejectReason, ok bool) {
	if o.State != StateFail || o.Reason == "" {
		return RejectReason{}, false
	}
	if err := json.Unmarshal([]byte(o.Reason), &reason); err != nil || reason.Label == "" {
		return RejectReason{}, false
	}
	return reason, true
}

// Trade is one fill, from GET /crossex/history_trades or "usertrades".
type Trade struct {
	UserID        string `json:"user_id"`
	TransactionID string `json:"transaction_id"`
	OrderID       string `json:"order_id"`
	Text          string `json:"text"`
	Symbol        string `json:"symbol"`
	ExchangeType  string `json:"exchange_type"`
	BusinessType  string `json:"business_type"`
	Side          string `json:"side"`
	Qty           string `json:"qty"`
	Price         string `json:"price"`
	Fee           string `json:"fee"`
	FeeCoin       string `json:"fee_coin"`
	FeeRate       string `json:"fee_rate"`
	MatchRole     string `json:"match_role"` // MAKER, TAKER
	Rpnl          string `json:"rpnl"`
	PositionMode  string `json:"position_mode"`
	PositionSide  string `json:"position_side"`
	CreateTime    string `json:"create_time"`
}

// OrderRequest is POST /crossex/orders. Optional fields are omitted when empty.
type OrderRequest struct {
	Text         string `json:"text,omitempty"`
	Symbol       string `json:"symbol"`
	Side         string `json:"side"`                    // BUY, SELL
	Type         string `json:"type,omitempty"`          // LIMIT (default), MARKET
	TimeInForce  string `json:"time_in_force,omitempty"` // GTC (default), IOC, FOK, POC, RPI
	Qty          string `json:"qty,omitempty"`
	Price        string `json:"price,omitempty"`
	QuoteQty     string `json:"quote_qty,omitempty"`
	ReduceOnly   string `json:"reduce_only,omitempty"`   // "true" / "false"
	PositionSide string `json:"position_side,omitempty"` // NONE (default), LONG, SHORT
}

// OrderAction is the response of order create and cancel. A create response
// only proves CrossEx accepted the request, not that the exchange did.
type OrderAction struct {
	OrderID string `json:"order_id"`
	Text    string `json:"text"`
}

// Leverage is the response of POST /crossex/positions/leverage.
type Leverage struct {
	Symbol   string `json:"symbol"`
	Leverage string `json:"leverage"`
}

// FeeRate is one exchange row of GET /crossex/fee.
type FeeRate struct {
	ExchangeType      string       `json:"exchange_type"`
	SpotMakerFee      string       `json:"spot_maker_fee"`
	SpotTakerFee      string       `json:"spot_taker_fee"`
	SpotRPIMakerFee   string       `json:"spot_rpi_maker_fee"`
	FutureMakerFee    string       `json:"future_maker_fee"`
	FutureTakerFee    string       `json:"future_taker_fee"`
	FutureRPIMakerFee string       `json:"future_rpi_maker_fee"`
	SpecialFeeList    []SpecialFee `json:"special_fee_list"`
}

type SpecialFee struct {
	Symbol       string `json:"symbol"`
	TakerFeeRate string `json:"taker_fee_rate"`
	MakerFeeRate string `json:"maker_fee_rate"`
}

// HistoryQuery pages history_orders and history_trades. Times are
// milliseconds; zero values are omitted.
type HistoryQuery struct {
	Symbol string
	From   int64
	To     int64
	Page   int
	Limit  int // history_trades: at most 1000
}

// OpenOrdersQuery filters GET /crossex/open_orders.
type OpenOrdersQuery struct {
	Symbol       string
	ExchangeType string
	BusinessType string
}

// PositionsQuery filters GET /crossex/positions.
type PositionsQuery struct {
	Symbol       string
	ExchangeType string
}
