package channels

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

type lifecycleTestChannel struct {
	started chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func (c *lifecycleTestChannel) Name() string                          { return "test" }
func (c *lifecycleTestChannel) AccountID() string                     { return "account" }
func (c *lifecycleTestChannel) BotUsername() string                   { return "" }
func (c *lifecycleTestChannel) Send(string, string) error             { return nil }
func (c *lifecycleTestChannel) SendMessage(bus.OutboundMessage) error { return nil }
func (c *lifecycleTestChannel) SendTyping(string) error               { return nil }
func (c *lifecycleTestChannel) Start(ctx context.Context) error {
	c.once.Do(func() { close(c.started) })
	<-ctx.Done()
	close(c.stopped)
	return nil
}

func TestManagerUnregisterStopsChannel(t *testing.T) {
	mb := bus.New()
	m := NewManager(mb)
	ch := &lifecycleTestChannel{started: make(chan struct{}), stopped: make(chan struct{})}
	m.RegisterSingleton(ch)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Start(ctx)

	select {
	case <-ch.started:
	case <-time.After(time.Second):
		t.Fatal("channel did not start")
	}
	m.Unregister(ch.Name(), ch.AccountID())
	select {
	case <-ch.stopped:
	case <-time.After(time.Second):
		t.Fatal("unregister did not cancel channel lifecycle")
	}
	if m.Has(ch.Name(), ch.AccountID()) {
		t.Fatal("unregistered channel still present")
	}
}
