package bingx

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// sign returns the hex-encoded HMAC-SHA256 signature Binance requires on
// every signed (TRADE/USER_DATA/USER_STREAM) request, computed over the raw
// query string exactly as it will be sent.
func sign(secret, query string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(query))
	return hex.EncodeToString(mac.Sum(nil))
}
