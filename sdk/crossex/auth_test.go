package crossex

import (
	"testing"
	"time"
)

// Vectors are the worked examples of the official APIv4 signature section
// (key "key", secret "secret"), which CrossEx reuses unchanged.
func TestSigningMatchesOfficialGETVector(t *testing.T) {
	payload := signingPayload("GET", "/api/v4/futures/orders", "contract=BTC_USD&status=finished&limit=50", "", "1541993715")
	want := "GET\n/api/v4/futures/orders\ncontract=BTC_USD&status=finished&limit=50\n" +
		"cf83e1357eefb8bdf1542850d66d8007d620e4050b5715dc83f4a921d36ce9ce47d0d13c5d85f2b0ff8318d2877eec2f63b931bd47417a81a538327af927da3e\n1541993715"
	if payload != want {
		t.Fatalf("payload = %q", payload)
	}
	if got := sign("secret", payload); got != "55f84ea195d6fe57ce62464daaa7c3c02fa9d1dde954e4c898289c9a2407a3d6fb3faf24deff16790d726b66ac9f74526668b13bd01029199cc4fcc522418b8a" {
		t.Fatalf("sign = %s", got)
	}
}

func TestSigningMatchesOfficialPOSTVector(t *testing.T) {
	body := `{"contract":"BTC_USD","type":"limit","size":100,"price":6800,"time_in_force":"gtc"}`
	if got := hashPayload(body); got != "ad3c169203dc3026558f01b4df307641fa1fa361f086b2306658886d5708767b1854797c68d9e62fef2f991645aa82673622ebf417e091d0bd22bafe5d956cca" {
		t.Fatalf("body hash = %s", got)
	}
	if got := sign("secret", signingPayload("post", "/api/v4/futures/orders", "", body, "1541993715")); got != "eae42da914a590ddf727473aff25fc87d50b64783941061f47a3fdb92742541fc4c2c14017581b4199a1418d54471c269c03a38d788d802e2c306c37636389f0" {
		t.Fatalf("sign = %s", got)
	}
}

func TestWSLoginSigningString(t *testing.T) {
	if got := wsSigningPayload("", "login", 1754272114); got != "channel=&event=login&time=1754272114" {
		t.Fatalf("ws payload = %q", got)
	}
	if got := sign("secret", wsSigningPayload("", "login", 1754272114)); got != "3f3a73022c2aa7d873a21d271e23e827373440365be942badd1ccb5290ea8b0d2f064485689baba80c12d2127ef90822f787ffd1039c6ba298e1e5d4a4ef3ba0" {
		t.Fatalf("ws sign = %s", got)
	}
}

func TestTimestampIsSeconds(t *testing.T) {
	if got := timestamp(time.Unix(1700000000, 999000000)); got != "1700000000" {
		t.Fatalf("timestamp = %s", got)
	}
}
