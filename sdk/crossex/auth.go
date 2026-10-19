package crossex

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CrossEx signs REST requests exactly like Gate APIv4:
//
//	HexEncode(HMAC_SHA512(secret, METHOD\nPATH\nQUERY\nHexEncode(SHA512(body))\nTIMESTAMP))
//
// and WebSocket logins with the string "channel=&event=login&time=<seconds>".

func sign(secret, payload string) string {
	mac := hmac.New(sha512.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func hashPayload(body string) string {
	sum := sha512.Sum512([]byte(body))
	return hex.EncodeToString(sum[:])
}

func timestamp(now time.Time) string {
	return strconv.FormatInt(now.Unix(), 10)
}

func signingPayload(method, requestPath, rawQuery, body, ts string) string {
	return strings.ToUpper(method) + "\n" + requestPath + "\n" + rawQuery + "\n" + hashPayload(body) + "\n" + ts
}

func wsSigningPayload(channel, event string, ts int64) string {
	return fmt.Sprintf("channel=%s&event=%s&time=%d", channel, event, ts)
}
