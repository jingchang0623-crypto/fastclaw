package channels

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// newTestWeChat builds a WeChat adapter wired to a stub CDN server.
// Only the fields the inbound path touches are populated.
func newTestWeChat(mb *bus.MessageBus, server *httptest.Server) *WeChat {
	return &WeChat{
		bus:        mb,
		accountID:  "bot_1",
		httpClient: server.Client(),
		cdnBaseURL: server.URL,
		ctxTokens:  make(map[string]wechatContextTokenState),
	}
}

func newTestFileMessage(fileItem *wechatFileItem) wechatMessage {
	return wechatMessage{
		MessageID:    42,
		FromUserID:   "user_1",
		MessageType:  wechatMsgTypeUser,
		MessageState: wechatMsgStateFinish,
		ItemList:     []wechatItem{{Type: wechatItemTypeFile, FileItem: fileItem}},
	}
}

func TestWeChatInboundFileIsDownloadedDecryptedAndForwarded(t *testing.T) {
	aesKey := []byte("0123456789abcdef")
	plaintext := []byte("%PDF-1.4 quarterly report body")
	ciphertext, err := wechatAESECBEncrypt(plaintext, aesKey)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/download" || r.URL.Query().Get("encrypted_query_param") != "param_1" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(ciphertext)
	}))
	defer server.Close()

	mb := bus.New()
	w := newTestWeChat(mb, server)
	w.dispatchInbound(newTestFileMessage(&wechatFileItem{
		FileName: "report.pdf",
		Len:      strconv.Itoa(len(plaintext)),
		Media: &wechatMediaInfo{
			EncryptQueryParam: "param_1",
			// Wire format: base64(hex(raw_key)) — see uploadToCDN.
			AESKey:      base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(aesKey))),
			EncryptType: wechatCDNEncryptType,
		},
	}))

	got := <-mb.Inbound
	if len(got.MediaItems) != 1 {
		t.Fatalf("media = %#v, want 1 item", got.MediaItems)
	}
	item := got.MediaItems[0]
	if item.Filename != "report.pdf" || item.ContentType != "application/pdf" || !bytes.Equal(item.Bytes, plaintext) {
		t.Fatalf("item = %q %q %q", item.Filename, item.ContentType, item.Bytes)
	}
	if got.Text == "" {
		t.Fatal("file-only message should carry a text cue for the agent")
	}
}

func TestWeChatInboundOversizedFileIsSkippedBeforeDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("oversized file must not be downloaded")
	}))
	defer server.Close()

	mb := bus.New()
	w := newTestWeChat(mb, server)
	w.dispatchInbound(newTestFileMessage(&wechatFileItem{
		FileName: "huge.zip",
		Len:      strconv.Itoa(wechatMaxInboundMediaBytes + 1),
		Media:    &wechatMediaInfo{EncryptQueryParam: "param_1", AESKey: "unused"},
	}))

	select {
	case got := <-mb.Inbound:
		t.Fatalf("unexpected inbound message: %#v", got)
	default:
	}
}

func TestWeChatInboundFileDownloadFailureIsDropped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer server.Close()

	mb := bus.New()
	w := newTestWeChat(mb, server)
	w.dispatchInbound(newTestFileMessage(&wechatFileItem{
		FileName: "report.docx",
		Media:    &wechatMediaInfo{EncryptQueryParam: "param_1", AESKey: "unused"},
	}))

	select {
	case got := <-mb.Inbound:
		t.Fatalf("unexpected inbound message: %#v", got)
	default:
	}
}
