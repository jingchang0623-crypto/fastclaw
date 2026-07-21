package channels

import (
	"testing"
	"time"
)

func TestWeChatCanSendRequiresFreshContextToken(t *testing.T) {
	w := &WeChat{ctxTokens: make(map[string]wechatContextTokenState)}
	if err := w.CanSend("boss"); err == nil {
		t.Fatal("missing context token should block proactive send")
	}
	w.ctxTokens["boss"] = wechatContextTokenState{
		Token:      "fresh",
		ReceivedAt: time.Now().Add(-time.Hour),
	}
	if err := w.CanSend("boss"); err != nil {
		t.Fatalf("fresh context token rejected: %v", err)
	}
	w.ctxTokens["boss"] = wechatContextTokenState{
		Token:      "stale",
		ReceivedAt: time.Now().Add(-24 * time.Hour),
	}
	if err := w.CanSend("boss"); err == nil {
		t.Fatal("stale context token should block proactive send")
	}
}
