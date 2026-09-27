package paystack

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"testing"
)

func TestValidSignature(t *testing.T) {
	c := New("sk_test_secret")
	body := []byte(`{"event":"charge.success","data":{"reference":"abc"}}`)
	mac := hmac.New(sha512.New, []byte("sk_test_secret"))
	mac.Write(body)
	good := hex.EncodeToString(mac.Sum(nil))

	if !c.ValidSignature(body, good) {
		t.Error("valid signature rejected")
	}
	if c.ValidSignature(body, "deadbeef") {
		t.Error("invalid signature accepted")
	}
	if c.ValidSignature([]byte(`{"event":"charge.success","data":{"reference":"xyz"}}`), good) {
		t.Error("signature of a different body accepted")
	}
}

func TestNewWithoutKey(t *testing.T) {
	if New("") != nil {
		t.Error("New with no key should return nil (payments off)")
	}
}
