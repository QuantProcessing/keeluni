package krakenspot

import (
	"encoding/json"
	"strings"
	"time"
)

// AssetPair is one row of AssetPairs. WSName ("XBT/USD") spells bitcoin XBT
// and dogecoin XDG; the v2 symbol is SymbolOf(WSName).
type AssetPair struct {
	Altname      string      `json:"altname"`
	WSName       string      `json:"wsname"`
	Base         string      `json:"base"`
	Quote        string      `json:"quote"`
	AClassBase   string      `json:"aclass_base"`
	PairDecimals int         `json:"pair_decimals"`
	LotDecimals  int         `json:"lot_decimals"`
	CostDecimals int         `json:"cost_decimals"`
	OrderMin     json.Number `json:"ordermin"`
	CostMin      json.Number `json:"costmin"`
	TickSize     json.Number `json:"tick_size"`
	Status       string      `json:"status"` // online, cancel_only, post_only, limit_only, reduce_only
}

// v2Names maps the legacy asset spellings of WSName onto WebSocket v2 and
// REST v2 symbols. Measured against the v2 instrument channel on
// 2026-10-20: these two cover every one of 1461 pairs.
var v2Names = map[string]string{"XBT": "BTC", "XDG": "DOGE"}

// AssetName spells a legacy asset name as v2 does.
func AssetName(name string) string {
	if v2, ok := v2Names[name]; ok {
		return v2
	}
	return name
}

// SymbolOf turns a WSName into the v2 symbol: "XBT/USD" -> "BTC/USD".
func SymbolOf(wsname string) string {
	base, quote, ok := strings.Cut(wsname, "/")
	if !ok {
		return wsname
	}
	return AssetName(base) + "/" + AssetName(quote)
}

// Asset is one row of Assets: Altname is the asset's display name with the
// legacy X/Z prefix removed ("XXBT" -> "XBT", "ZUSD" -> "USD").
type Asset struct {
	Altname string `json:"altname"`
	Status  string `json:"status"`
}

// BalanceAsset names a balance key in v2 spelling using the Assets table.
// Earn and staking balances (".F", ".S", ".M", ".B", ".T") are not tradable
// spot balances and report false.
func BalanceAsset(key string, assets map[string]Asset) (string, bool) {
	if strings.Contains(key, ".") {
		return "", false
	}
	if asset, ok := assets[key]; ok && asset.Altname != "" {
		return AssetName(asset.Altname), true
	}
	return AssetName(key), true
}

type Level [3]json.Number // price, volume, timestamp

type Depth struct {
	Asks []Level `json:"asks"`
	Bids []Level `json:"bids"`
}

type OHLC struct {
	Time   int64
	Open   string
	High   string
	Low    string
	Close  string
	Volume string
}

type PublicTrade struct {
	Price   string
	Volume  string
	Side    string // b, s
	Time    time.Time
	TradeID int64
}

type ExtendedBalance struct {
	Balance   json.Number `json:"balance"`
	Credit    json.Number `json:"credit"`
	CreditUse json.Number `json:"credit_used"`
	HoldTrade json.Number `json:"hold_trade"`
}

type OrderDescription struct {
	Pair      string `json:"pair"`
	Type      string `json:"type"`      // buy, sell
	OrderType string `json:"ordertype"` // market, limit, ...
	Price     string `json:"price"`
}

// OrderInfo is an open, closed or queried order.
type OrderInfo struct {
	ClientOrderID string           `json:"cl_ord_id"`
	Status        string           `json:"status"` // pending, open, closed, canceled, expired
	Reason        *string          `json:"reason"`
	OpenTime      json.Number      `json:"opentm"`
	CloseTime     json.Number      `json:"closetm"`
	Description   OrderDescription `json:"descr"`
	Volume        json.Number      `json:"vol"`
	VolumeExec    json.Number      `json:"vol_exec"`
	Cost          json.Number      `json:"cost"`
	Fee           json.Number      `json:"fee"`
	Price         json.Number      `json:"price"` // average fill price
	Flags         string           `json:"oflags"`
}

func (o OrderInfo) Opened() time.Time { return secondsTime(o.OpenTime) }
func (o OrderInfo) Closed() time.Time { return secondsTime(o.CloseTime) }

// OwnTrade is a fill from TradesHistory. Pair is the legacy REST key.
type OwnTrade struct {
	OrderTxID string      `json:"ordertxid"`
	Pair      string      `json:"pair"`
	Time      json.Number `json:"time"`
	Type      string      `json:"type"`
	OrderType string      `json:"ordertype"`
	Price     json.Number `json:"price"`
	Cost      json.Number `json:"cost"`
	Fee       json.Number `json:"fee"`
	Volume    json.Number `json:"vol"`
	Maker     bool        `json:"maker"`
	TradeID   json.Number `json:"trade_id"`
}

func (t OwnTrade) At() time.Time { return secondsTime(t.Time) }

// OrderRequest is AddOrder. Side buy|sell; Type market|limit; TimeInForce
// GTC|IOC; Flags "post" for post-only. ClientOrderID is free text of at most
// 18 ASCII characters (or a UUID).
type OrderRequest struct {
	Pair          string
	Side          string
	Type          string
	Volume        string
	Price         string
	TimeInForce   string
	Flags         string
	ClientOrderID string
}

type AddOrderResult struct {
	TxID        []string `json:"txid"`
	Description struct {
		Order string `json:"order"`
	} `json:"descr"`
}
