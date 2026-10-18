package crossex

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// APIError is a non-2xx REST response. Label is CrossEx's stable error
// identifier; Message is for humans and must not drive control flow.
type APIError struct {
	StatusCode int
	Method     string
	Path       string
	Label      string
	Message    string
	Body       string
}

func (e *APIError) Error() string {
	if e.Label != "" || e.Message != "" {
		return fmt.Sprintf("crossex sdk: %s %s returned %d: %s %s", e.Method, e.Path, e.StatusCode, e.Label, e.Message)
	}
	return fmt.Sprintf("crossex sdk: %s %s returned %d", e.Method, e.Path, e.StatusCode)
}

func newAPIError(statusCode int, method, path string, body []byte) error {
	apiErr := &APIError{StatusCode: statusCode, Method: method, Path: path, Body: string(body)}
	var payload struct {
		Label   string `json:"label"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if json.Unmarshal(body, &payload) == nil {
		apiErr.Label = payload.Label
		apiErr.Message = payload.Message
		if apiErr.Message == "" {
			apiErr.Message = payload.Detail
		}
	}
	return apiErr
}

// IsOrderNotFound reports whether CrossEx stated the order does not exist (or
// can no longer be found by its client text).
func IsOrderNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && strings.EqualFold(apiErr.Label, "TRADE_ORDER_NOT_FOUND_ERROR")
}

// IsCredentialRejection reports a key, signature, permission or IP failure.
func IsCredentialRejection(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch strings.ToUpper(apiErr.Label) {
	case "INVALID_KEY", "INVALID_SIGNATURE", "INVALID_CREDENTIALS", "MISSING_REQUIRED_HEADER",
		"REQUEST_EXPIRED", "IP_FORBIDDEN", "FORBIDDEN", "READ_ONLY", "ACCOUNT_LOCKED", "USER_NOT_EXIST":
		return true
	}
	return apiErr.StatusCode == http.StatusUnauthorized
}

// IsDefinitiveCommandRejection reports whether err proves CrossEx received a
// command and refused it without acting. It is an allowlist: an unknown label
// cannot prove non-execution. TRADE_ORDER_DUPLICATE_ERROR is deliberately
// absent — it means an earlier attempt with the same text may have been
// accepted and must be recovered by lookup.
func IsDefinitiveCommandRejection(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Label == "" {
		return false
	}
	if apiErr.StatusCode < http.StatusBadRequest || apiErr.StatusCode >= http.StatusInternalServerError {
		return false
	}
	switch apiErr.StatusCode {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests:
		return false
	}
	if IsCredentialRejection(err) {
		return true
	}
	_, ok := definitiveRejectionLabels[strings.ToUpper(apiErr.Label)]
	return ok
}

// From the CrossEx v1.0.6 error label table: parameter, symbol, risk and
// margin refusals that are decided before any exchange interaction.
var definitiveRejectionLabels = map[string]struct{}{
	"COMMON_PARAM_BIND_ERROR":                           {},
	"COMMON_MISSING_PARAM_ERROR":                        {},
	"TRADE_UNSUPPORTED_OPERATION":                       {},
	"NOT_BEST_ACCOUNT_ROUTER":                           {},
	"TRADE_CLIENT_ORDER_ID_LENGTH_ERROR":                {},
	"TRADE_CLIENT_ORDER_ID_MATCH_ERROR":                 {},
	"TRADE_INVALID_SIDE":                                {},
	"TRADE_INVALID_ORDER_TYPE":                          {},
	"TRADE_INVALID_TIME_IN_FORCE":                       {},
	"TRADE_INVALID_POC":                                 {},
	"TRADE_INVALID_ORDER_QTY":                           {},
	"TRADE_INVALID_QUOTE_ORDER_QTY":                     {},
	"TRADE_INVALID_LIMIT_PRICE":                         {},
	"TRADE_INVALID_REDUCE_ONLY":                         {},
	"TRADE_SYM_NOT_SUPPORT":                             {},
	"TRADE_NOT_SUPPORT_SYMBOLS_ERROR":                   {},
	"TRADE_INVALID_POSITION_SIDE":                       {},
	"TRADE_INVALID_PZ_SIDE_ERROR":                       {},
	"TRADE_INVALID_EXCHANGE_TYPE":                       {},
	"TRADE_INVALID_SYM_BUSINESSTYPE_ERROR":              {},
	"TRADE_ORDER_LOT_SIZE_ERROR":                        {},
	"TRADE_ORDER_QUANTITY_MAX_ERROR":                    {},
	"TRADE_ORDER_QUANTITY_MIN_ERROR":                    {},
	"TRADE_ORDER_TICK_SIZE_ERROR":                       {},
	"TRADE_ORDER_AMOUNT_MIN_ERROR":                      {},
	"TRADE_MAX_ORDERS_ERROR":                            {},
	"TRADE_EXCEED_MAX_NOTIONAL":                         {},
	"TRADE_INSUFFICIENT_AVAILABLE_ERROR":                {},
	"TRADE_INSUFFICIENT_AVAILABLE_MARGIN_ERROR":         {},
	"TRADE_POSITION_EXCEED_MAX_NOTIONAL_LIMIT":          {},
	"TRADE_REDUCE_ONLY_CANNOT_PLACE_IN_THIS_PRICE":      {},
	"TRADE_REDUCE_ONLY_POSITION_QTY_ERROR":              {},
	"TRADE_REDUCE_ONLY_SIDE_ERROR":                      {},
	"TRADE_REDUCE_TOO_SMALL_ERROR":                      {},
	"TRADE_BOTH_REDUCE_ERROR":                           {},
	"TRADE_SET_LEVERAGE_ERROR":                          {},
	"TRADE_SET_LEVERAGE_EXCEED_LIMIT_ERROR":             {},
	"TRADE_LEVERAGE_MARGIN_OPEN_ERROR":                  {},
	"TRADE_LEVERAGE_MARGIN_REVERSE_OPEN_ERROR":          {},
	"RISK_CHECK_MARKET_FORBIDDEN":                       {},
	"TRADE_DELIST_OPEN_PROHIBITED_ERROR":                {},
	"TRADE_RPI_EXCHANGE_NOT_SUPPORT":                    {},
	"TRADE_RPI_LIMIT_ORDER_REQUIRED":                    {},
	"TRADE_RPI_SYMBOL_NOT_SUPPORT":                      {},
	"TRADE_RPI_USER_NOT_SUPPORT":                        {},
	"TRADE_OKX_LIMIT_BUY_ORDER_PRICE_ERROR":             {},
	"TRADE_HYPERLIQUID_ORDER_LIMIT_ERROR":               {},
	"TRADE_LIGHTER_ORDER_LIMIT_ERROR":                   {},
	"TRADE_HYPERLIQUID_PRICE_SIGNIFICANT_FIGURES_ERROR": {},
	"TRADE_ORDER_NOT_FOUND_ERROR":                       {},
	"TRADE_ONE_OF_ORDER_ID_ERROR":                       {},
	"TRADE_ORDER_ID_ERROR":                              {},
	"TRADE_ORDER_ID_LENGTH":                             {},
	"TRADE_ASSET_NOT_EXISTS_ERROR":                      {},
}
