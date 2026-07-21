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

func TestEngagementDueAtAvoidsQuietHours(t *testing.T) {
	shanghai := time.FixedZone("Asia/Shanghai", 8*60*60)
	received := time.Date(2026, 7, 21, 7, 0, 0, 0, shanghai)
	due := engagementDueAt(received, shanghai).In(shanghai)
	if due.Hour() != 21 || due.Day() != 21 {
		t.Fatalf("due = %v, want same-day 21:00", due)
	}
	if age := due.Sub(received); age >= wechatContextTokenMaxAge {
		t.Fatalf("engagement due after send window: %v", age)
	}
}

func TestFireDueEngagementsFiresOnlyOnce(t *testing.T) {
	now := time.Now().UTC()
	w := &WeChat{
		accountID: "boss-bot",
		ctxTokens: map[string]wechatContextTokenState{
			"boss": {Token: "valid", ReceivedAt: now.Add(-19 * time.Hour)},
		},
	}
	var calls int
	w.SetOnEngagementDue(func(accountID, chatID string) {
		calls++
		if accountID != "boss-bot" || chatID != "boss" {
			t.Fatalf("unexpected callback target %s/%s", accountID, chatID)
		}
	})
	w.fireDueEngagements(now)
	w.fireDueEngagements(now.Add(time.Minute))
	if calls != 1 {
		t.Fatalf("engagement calls = %d, want 1", calls)
	}
}

func TestFireDueEngagementsDeduplicatesDelayedReport(t *testing.T) {
	now := time.Now().UTC()
	received := now.Add(-19 * time.Hour)
	w := &WeChat{
		accountID: "boss-bot",
		ctxTokens: map[string]wechatContextTokenState{
			"boss": {
				Token:          "valid",
				ReceivedAt:     received,
				LastOutboundAt: received.Add(8 * time.Hour),
			},
		},
	}
	var calls int
	w.SetOnEngagementDue(func(string, string) { calls++ })
	w.fireDueEngagements(now)
	if calls != 0 {
		t.Fatalf("engagement calls = %d, want delayed report to suppress it", calls)
	}
	if w.ctxTokens["boss"].LastEngagementAt.IsZero() {
		t.Fatal("delayed report was not persisted as this cycle's engagement")
	}
}
