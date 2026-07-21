package gateway

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/channels"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// A production regression once left every persisted WeChat account offline
// after a process restart. This test protects the boot-time contract: enabled
// channel rows must be registered without requiring a dashboard visit.
func TestRegisterChannelsFromStoreRestoresEnabledWeChatAccount(t *testing.T) {
	ctx := context.Background()
	db, err := store.NewDBStore("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	const accountID = "boss-bot@im.bot"
	if err := db.SaveChannel(ctx, &store.ChannelRecord{
		ID:        "ch_wechat_recovery",
		UserID:    "u_boss",
		AgentID:   "agt_advisor",
		Type:      "wechat",
		AccountID: accountID,
		Enabled:   true,
		BotToken:  "test-token",
		Data: map[string]interface{}{
			"accounts": map[string]interface{}{
				accountID: map[string]interface{}{
					"botToken": "test-token",
					"baseUrl":  "https://ilinkai.weixin.qq.com",
					"userId":   "boss@im.wechat",
				},
			},
		},
	}); err != nil {
		t.Fatalf("save channel: %v", err)
	}

	mb := bus.New()
	mgr := channels.NewManager(mb)
	if err := registerChannelsFromStore(db, mb, mgr); err != nil {
		t.Fatalf("register channels: %v", err)
	}
	if !mgr.Has("wechat", accountID) {
		t.Fatal("enabled WeChat account was not restored from the channel store")
	}
}
