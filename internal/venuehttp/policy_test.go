package venuehttp

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type policyRoundTrip func(*http.Request) (*http.Response, error)

func (fn policyRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
func TestWirePoliciesWeightParametersAndPreserveBodies(t *testing.T) {
	tests := []struct {
		venue, method, path, body, action string
		cost                              int
	}{
		{"binance", "GET", "/api/v3/depth?limit=100", "", "read", 5},
		{"binance", "GET", "/api/v3/depth?limit=5000", "", "read", 250},
		{"binance", "GET", "/fapi/v1/depth", "", "read", 10},
		{"binance", "GET", "/fapi/v1/klines?limit=1500", "", "read", 10},
		{"binance", "GET", "/api/v3/ticker/24hr", "", "read", 80},
		{"binance", "GET", "/api/v3/ticker/24hr?symbol=BTCUSDT", "", "read", 2},
		{"binance", "DELETE", "/api/v3/order?signature=SECRET", "", "cancel", 1},
		{"binance", "POST", "/fapi/v1/algoOrder", "", "place", 1},
		{"okx", "POST", "/api/v5/trade/order-algo", "", "place", 1},
		{"gate", "POST", "/api/v4/futures/usdt/price_orders", "", "place", 1},
		{"gate", "DELETE", "/api/v4/futures/usdt/price_orders/12", "", "cancel", 1},
		{"gate", "DELETE", "/api/v4/futures/usdt/price_orders/13", "", "cancel", 1},
		{"okx", "POST", "/api/v5/trade/cancel-algos", "", "cancel", 1},
		{"binance", "DELETE", "/fapi/v1/algoOrder?algoId=1", "", "cancel", 1},
		{"binance", "GET", "/fapi/v1/openAlgoOrders", "", "read", 40},
		{"binance", "GET", "/fapi/v1/openAlgoOrders?symbol=BTCUSDT", "", "read", 1},
		{"bybit", "POST", "/v5/order/create-batch", `{"request":[{},{}]}`, "place", 2},
		{"hyperliquid", "POST", "/info", `{"type":"allMids"}`, "read", 2},
		{"hyperliquid", "POST", "/info", `{"type":"userFillsByTime"}`, "read", 120},
		{"hyperliquid", "POST", "/info", `{"type":"metaAndAssetCtxs"}`, "read", 20},
		{"hyperliquid", "POST", "/exchange", `{"action":{"type":"cancel","cancels":[{}]}}`, "cancel", 1},
		{"nado", "POST", "/v1/execute", `{"place_order":{}}`, "place", 1},
		{"lighter", "POST", "/api/v1/sendTx", `{}`, "", 1},
		{"kraken", "POST", "/derivatives/api/v3/sendorder", `symbol=PF_XBTUSD`, "place", 10},
		{"kraken", "POST", "/derivatives/api/v3/cancelorder", `order_id=1`, "cancel", 10},
		{"kraken", "GET", "/derivatives/api/v3/openpositions", "", "read", 1},
		{"deribit", "GET", "/api/v2/private/buy?amount=1", "", "place", 1},
		{"deribit", "GET", "/api/v2/private/cancel?order_id=1", "", "cancel", 1},
		{"deribit", "GET", "/api/v2/public/ticker", "", "read", 1},
	}
	for _, tt := range tests {
		t.Run(tt.venue+tt.path, func(t *testing.T) {
			client := &http.Client{Transport: policyRoundTrip(func(req *http.Request) (*http.Response, error) {
				p := PolicyForRequest(req)
				if p.Venue != tt.venue || p.Cost != tt.cost || p.Action != tt.action || strings.Contains(p.Endpoint, "SECRET") {
					t.Fatalf("policy=%+v", p)
				}
				body, _ := io.ReadAll(req.Body)
				if string(body) != tt.body {
					t.Fatalf("body=%s", body)
				}
				return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
			})}
			req, _ := http.NewRequest(tt.method, "https://example.invalid"+tt.path, strings.NewReader(tt.body))
			resp, err := DoHTTP(client, req, tt.venue)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
		})
	}
}
func TestSDKTransportFailureKeepsClassificationWithoutRenderingSignedURL(t *testing.T) {
	native := &LocalRateLimitError{Action: "place"}
	client := &http.Client{Transport: policyRoundTrip(func(*http.Request) (*http.Response, error) { return nil, native })}
	req, _ := http.NewRequest("POST", "https://example.invalid/order?signature=SECRET", nil)
	_, err := DoHTTP(client, req, "binance")
	var local *LocalRateLimitError
	if !errors.As(err, &local) || local != native || !errors.Is(err, ErrRateLimited) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err=%v", err)
	}
}
