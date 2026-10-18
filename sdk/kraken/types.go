package kraken

import (
	"encoding/json"
	"strings"
)

// Instrument is one row of GET /instruments.
type Instrument struct {
	Symbol                      string      `json:"symbol"`
	Type                        string      `json:"type"` // flexible_futures, futures_inverse, ...
	Base                        string      `json:"base"`
	Quote                       string      `json:"quote"`
	Pair                        string      `json:"pair"`
	TickSize                    json.Number `json:"tickSize"`
	ContractSize                json.Number `json:"contractSize"`
	ContractValueTradePrecision json.Number `json:"contractValueTradePrecision"`
	MaxPositionSize             json.Number `json:"maxPositionSize"`
	Tradeable                   bool        `json:"tradeable"`
	Suspended                   bool        `json:"suspended"`
	PostOnly                    bool        `json:"postOnly"`
	IsExpired                   bool        `json:"isExpired"`
	TradFi                      bool        `json:"tradfi"`
	Category                    string      `json:"category"`
	Tags                        []string    `json:"tags"`
}

// Ticker is one row of GET /tickers.
type Ticker struct {
	Symbol              string      `json:"symbol"`
	Tag                 string      `json:"tag"`
	Pair                string      `json:"pair"`
	Last                json.Number `json:"last"`
	LastTime            string      `json:"lastTime"`
	MarkPrice           json.Number `json:"markPrice"`
	IndexPrice          json.Number `json:"indexPrice"`
	Bid                 json.Number `json:"bid"`
	BidSize             json.Number `json:"bidSize"`
	Ask                 json.Number `json:"ask"`
	AskSize             json.Number `json:"askSize"`
	Vol24h              json.Number `json:"vol24h"`
	Open24h             json.Number `json:"open24h"`
	High24h             json.Number `json:"high24h"`
	Low24h              json.Number `json:"low24h"`
	OpenInterest        json.Number `json:"openInterest"`
	FundingRate         json.Number `json:"fundingRate"`
	RelativeFundingRate json.Number `json:"relativeFundingRate"`
	Suspended           bool        `json:"suspended"`
	PostOnly            bool        `json:"postOnly"`
}

// OrderBook is GET /orderbook; levels are [price, size].
type OrderBook struct {
	Bids       [][2]json.Number `json:"bids"`
	Asks       [][2]json.Number `json:"asks"`
	ServerTime string           `json:"-"`
}

type PublicTrade struct {
	TradeID int64       `json:"trade_id"`
	Price   json.Number `json:"price"`
	Size    json.Number `json:"size"`
	Side    string      `json:"side"` // buy, sell (taker side)
	Type    string      `json:"type"` // fill, liquidation, ...
	Time    string      `json:"time"`
	UID     string      `json:"uid"`
}

// Candle is one charts-API candle; prices are decimal strings, time is ms.
type Candle struct {
	Time   int64  `json:"time"`
	Open   string `json:"open"`
	High   string `json:"high"`
	Low    string `json:"low"`
	Close  string `json:"close"`
	Volume string `json:"volume"`
}

// FundingRate is one historical funding row. RelativeFundingRate is the rate
// as a fraction of position value per funding period (hourly).
type FundingRate struct {
	Timestamp           string      `json:"timestamp"`
	FundingRate         json.Number `json:"fundingRate"`
	RelativeFundingRate json.Number `json:"relativeFundingRate"`
}

// Accounts is GET /accounts. "flex" is the multi-collateral margin account
// that backs PF_ perpetuals; other entries are cash and single-collateral
// accounts.
type Accounts struct {
	Flex *FlexAccount `json:"flex"`
}

type FlexAccount struct {
	Type                    string                  `json:"type"`
	Currencies              map[string]FlexCurrency `json:"currencies"`
	BalanceValue            json.Number             `json:"balanceValue"`
	PortfolioValue          json.Number             `json:"portfolioValue"`
	CollateralValue         json.Number             `json:"collateralValue"`
	InitialMargin           json.Number             `json:"initialMargin"`
	InitialMarginWithOrders json.Number             `json:"initialMarginWithOrders"`
	MaintenanceMargin       json.Number             `json:"maintenanceMargin"`
	PnL                     json.Number             `json:"pnl"`
	UnrealizedFunding       json.Number             `json:"unrealizedFunding"`
	TotalUnrealized         json.Number             `json:"totalUnrealized"`
	MarginEquity            json.Number             `json:"marginEquity"`
	AvailableMargin         json.Number             `json:"availableMargin"`
}

type FlexCurrency struct {
	Quantity   json.Number `json:"quantity"`
	Value      json.Number `json:"value"`
	Collateral json.Number `json:"collateral"`
	Available  json.Number `json:"available"`
}

// OpenPosition is one row of GET /openpositions. Size is unsigned; Side says
// long or short. Price is the average entry price.
type OpenPosition struct {
	Side              string      `json:"side"`
	Symbol            string      `json:"symbol"`
	Price             json.Number `json:"price"`
	Size              json.Number `json:"size"`
	FillTime          string      `json:"fillTime"`
	UnrealizedFunding json.Number `json:"unrealizedFunding"`
	PnlCurrency       string      `json:"pnlCurrency"`
	MaxFixedLeverage  json.Number `json:"maxFixedLeverage"`
}

// OpenOrder is one row of GET /openorders. Status is untouched or
// partiallyFilled.
type OpenOrder struct {
	OrderID        string      `json:"order_id"`
	ClientOrderID  string      `json:"cliOrdId"`
	Status         string      `json:"status"`
	Side           string      `json:"side"`
	OrderType      string      `json:"orderType"` // lmt, post, ioc, mkt, stp, take_profit
	Symbol         string      `json:"symbol"`
	LimitPrice     json.Number `json:"limitPrice"`
	FilledSize     json.Number `json:"filledSize"`
	UnfilledSize   json.Number `json:"unfilledSize"`
	ReduceOnly     bool        `json:"reduceOnly"`
	ReceivedTime   string      `json:"receivedTime"`
	LastUpdateTime string      `json:"lastUpdateTime"`
}

type Fill struct {
	FillID        string      `json:"fill_id"`
	Symbol        string      `json:"symbol"`
	Side          string      `json:"side"`
	OrderID       string      `json:"order_id"`
	ClientOrderID string      `json:"cliOrdId"`
	Size          json.Number `json:"size"`
	Price         json.Number `json:"price"`
	FillTime      string      `json:"fillTime"`
	FillType      string      `json:"fillType"` // maker, taker, liquidation, ...
}

// OrderRequest is POST /sendorder. OrderType is lmt, post, ioc or mkt.
type OrderRequest struct {
	OrderType     string
	Symbol        string
	Side          string // buy, sell
	Size          string
	LimitPrice    string
	ClientOrderID string
	ReduceOnly    bool
}

// SendStatus.Status is "placed" (accepted) or a rejection reason such as
// insufficientAvailableFunds, iocWouldNotExecute, postWouldExecute.
type SendStatus struct {
	OrderID       string       `json:"order_id"`
	ClientOrderID string       `json:"cliOrdId"`
	Status        string       `json:"status"`
	ReceivedTime  string       `json:"receivedTime"`
	OrderEvents   []OrderEvent `json:"orderEvents"`
}

// OrderEvent is one PLACE, EXECUTION, REJECT, CANCEL or EDIT event.
type OrderEvent struct {
	Type                string      `json:"type"`
	Reason              string      `json:"reason"`
	Price               json.Number `json:"price"`
	Amount              json.Number `json:"amount"`
	ExecutionID         string      `json:"executionId"`
	Order               *EventOrder `json:"order"`
	OrderPriorExecution *EventOrder `json:"orderPriorExecution"`
}

type EventOrder struct {
	OrderID             string      `json:"orderId"`
	ClientOrderID       string      `json:"cliOrdId"`
	Type                string      `json:"type"`
	Symbol              string      `json:"symbol"`
	Side                string      `json:"side"`
	Quantity            json.Number `json:"quantity"`
	Filled              json.Number `json:"filled"`
	LimitPrice          json.Number `json:"limitPrice"`
	ReduceOnly          bool        `json:"reduceOnly"`
	Timestamp           string      `json:"timestamp"`
	LastUpdateTimestamp string      `json:"lastUpdateTimestamp"`
}

// CancelStatus.Status is cancelled, filled or notFound.
type CancelStatus struct {
	OrderID      string       `json:"order_id"`
	Status       string       `json:"status"`
	ReceivedTime string       `json:"receivedTime"`
	OrderEvents  []OrderEvent `json:"orderEvents"`
}

// OrderStatusDetails is one row of POST /orders/status. Status is
// ENTERED_BOOK, FULLY_EXECUTED, REJECTED, CANCELLED, ...
type OrderStatusDetails struct {
	Order        EventOrder `json:"order"`
	Status       string     `json:"status"`
	UpdateReason string     `json:"updateReason"`
	Error        string     `json:"error"`
}

type LeveragePreference struct {
	Symbol      string      `json:"symbol"`
	MaxLeverage json.Number `json:"maxLeverage"`
}

// IsAccepted reports a placement Kraken accepted.
func (s SendStatus) IsAccepted() bool {
	return s.Status == "placed" || s.Status == "partiallyFilled" || s.Status == "filled"
}

// Linear reports a multi-collateral linear perpetual (PF_*).
func (i Instrument) Linear() bool {
	return i.Type == "flexible_futures" && strings.HasPrefix(i.Symbol, "PF_")
}
