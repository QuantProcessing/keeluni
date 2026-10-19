package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/QuantProcessing/keeluni/gateway/account"
	"github.com/QuantProcessing/keeluni/gateway/funding"
	"github.com/QuantProcessing/keeluni/gateway/hub"
	"github.com/QuantProcessing/keeluni/gateway/market"
	"github.com/QuantProcessing/keeluni/gateway/resident"
	"github.com/QuantProcessing/keeluni/gateway/store"
	"github.com/QuantProcessing/keeluni/secretstore"
	"github.com/shopspring/decimal"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	cipher, _ := secretstore.NewCipher(bytes.Repeat([]byte{7}, 32))
	accounts, err := store.Open(filepath.Join(t.TempDir(), "accounts.enc"), cipher)
	if err != nil {
		t.Fatal(err)
	}
	catalog := market.NewStaticCatalog([]exchange.Instrument{{
		Identity: exchange.InstrumentIdentity{Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Symbol: "BTC-USDT",
			BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT", NativeSymbol: "BTCUSDT"},
		Rules: exchange.InstrumentRules{PriceIncrement: decimal.RequireFromString("0.1"), QuantityIncrement: decimal.RequireFromString("0.001"), MinQuantity: decimal.RequireFromString("0.001")},
	}})
	manager := account.NewManager(accounts, catalog, http.DefaultClient)
	residents := resident.NewRegistry()
	fundingData, err := funding.New(catalog, residents, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	feeds := market.NewFeeds(catalog, fundingData)
	public := hub.New(feeds.Source, time.Second)
	private := hub.New(manager.Source, time.Second)
	datasets := hub.New(fundingData.Source, time.Second)
	t.Cleanup(func() { public.Close(); private.Close(); datasets.Close() })
	return NewServer([]byte("test-token-0123456789abcdef0123456789"), Deps{
		Catalog: catalog, Accounts: manager, Feeds: feeds, Funding: fundingData, Resident: residents,
		Public: public, Private: private, Datasets: datasets,
	})
}

func do(server *Server, method, path, token, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	return recorder
}

const token = "test-token-0123456789abcdef0123456789"

func TestAuthentication(t *testing.T) {
	server := newTestServer(t)
	if got := do(server, "GET", "/v1/health", "", "").Code; got != 200 {
		t.Fatalf("health = %d", got)
	}
	for _, bad := range []string{"", "wrong"} {
		if got := do(server, "GET", "/v1/instruments", bad, "").Code; got != 401 {
			t.Fatalf("token %q = %d", bad, got)
		}
	}
	response := do(server, "GET", "/v1/instruments", token, "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"id":"binance:perp:BTC-USDT"`) {
		t.Fatalf("instruments = %d %s", response.Code, response.Body)
	}
}

func TestAccountErrors(t *testing.T) {
	server := newTestServer(t)
	cases := []struct {
		method, path, body string
		want               int
	}{
		{"GET", "/v1/accounts/acc_missing/positions", "", 404},
		{"POST", "/v1/accounts/acc_missing/orders", `{"inst":"binance:perp:BTC-USDT","side":"buy","type":"market","qty":"1","clientOrderId":"1"}`, 404},
		{"POST", "/v1/accounts", `{"venue":"nope","credentials":{}}`, 400},
		{"POST", "/v1/accounts", `{"venue":"binance","credentials":{"apiKey":"k"}}`, 400},
		{"POST", "/v1/accounts", `{"venue":"binance","credentials":{"apiKey":"k","apiSecret":"s","extra":"x"}}`, 400},
		{"POST", "/v1/accounts", `{"venue":"binance","unexpected":1}`, 400},
		{"DELETE", "/v1/accounts/acc_missing", "", 404},
	}
	for _, tc := range cases {
		response := do(server, tc.method, tc.path, token, tc.body)
		if response.Code != tc.want {
			t.Errorf("%s %s = %d %s, want %d", tc.method, tc.path, response.Code, response.Body, tc.want)
		}
		if strings.Contains(response.Body.String(), `"s"`) && strings.Contains(tc.body, "apiSecret") {
			t.Errorf("response echoes credential material: %s", response.Body)
		}
	}
}

func TestVenuesListBrokerWithItsMarkets(t *testing.T) {
	server := newTestServer(t)
	response := do(server, "GET", "/v1/venues", token, "")
	var venues []struct {
		Venue   string   `json:"venue"`
		Kind    string   `json:"kind"`
		Markets []string `json:"markets"`
		Fields  []struct {
			Key string `json:"key"`
		} `json:"fields"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &venues) != nil {
		t.Fatalf("venues = %d %s", response.Code, response.Body)
	}
	var crossex, binance bool
	for _, item := range venues {
		switch item.Venue {
		case "crossex":
			crossex = item.Kind == "broker" && len(item.Fields) == 2 && strings.Contains(strings.Join(item.Markets, ","), "kraken:perp:USD")
		case "binance":
			binance = item.Kind == "native"
		}
	}
	if !crossex || !binance {
		t.Fatalf("venues = %+v", venues)
	}
	for _, path := range []string{"/v1/accounts/acc_missing/margin", "/v1/accounts/acc_missing/instruments"} {
		if got := do(server, "GET", path, token, "").Code; got != 404 {
			t.Fatalf("%s = %d", path, got)
		}
	}
}

func TestFundingEndpoints(t *testing.T) {
	server := newTestServer(t)
	// The test catalog has one perp; nothing has been collected yet.
	if response := do(server, "GET", "/v1/funding", token, ""); response.Code != 200 || !strings.Contains(response.Body.String(), `"quotes":[]`) ||
		!strings.Contains(response.Body.String(), `"market":"binance:perp:USDT"`) {
		t.Fatalf("table = %d %s", response.Code, response.Body)
	}
	if got := do(server, "GET", "/v1/funding?inst=binance:perp:BTC-USDT", token, "").Code; got != 404 {
		t.Fatalf("uncollected quote = %d", got)
	}
	if got := do(server, "GET", "/v1/funding?inst=binance:perp:NOPE-USDT", token, "").Code; got != 404 {
		t.Fatalf("unknown instrument = %d", got)
	}
	if got := do(server, "GET", "/v1/funding/history?inst=binance:perp:BTC-USDT&since=1&until=4000000000000", token, "").Code; got != 400 {
		t.Fatalf("oversized window = %d", got)
	}
	health := do(server, "GET", "/v1/health", "", "")
	if health.Code != 200 || !strings.Contains(health.Body.String(), `"datasets"`) {
		t.Fatalf("health = %d %s", health.Code, health.Body)
	}
}
