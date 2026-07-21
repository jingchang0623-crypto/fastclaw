package channels

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
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

func TestWeChatOutboundFileIsEncryptedAndSent(t *testing.T) {
	var mu sync.Mutex
	var uploadReq wechatGetUploadURLRequest
	var sendReq wechatSendRequest
	var encryptedUpload []byte

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/getuploadurl":
			mu.Lock()
			err := json.NewDecoder(r.Body).Decode(&uploadReq)
			mu.Unlock()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(wechatGetUploadURLResponse{
				Ret:           0,
				UploadFullURL: server.URL + "/cdn-upload",
			})
		case "/cdn-upload":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			encryptedUpload = body
			mu.Unlock()
			w.Header().Set("X-Encrypted-Param", "download-token")
		case "/ilink/bot/sendmessage":
			mu.Lock()
			err := json.NewDecoder(r.Body).Decode(&sendReq)
			mu.Unlock()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(wechatSendResponse{Ret: 0})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	wc := newTestWeChat(bus.New(), server)
	wc.baseURL = server.URL
	wc.botToken = "bot-token"
	wc.wechatUIN = "test-uin"
	payload := []byte("pdf-data")
	if err := wc.SendMessage(bus.OutboundMessage{
		ChatID: "user_1",
		MediaItems: []bus.MediaItem{{
			Filename:    "report.pdf",
			ContentType: "application/pdf",
			Bytes:       payload,
		}},
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if uploadReq.MediaType != wechatCDNMediaTypeFile {
		t.Fatalf("CDN media type = %d, want file type %d", uploadReq.MediaType, wechatCDNMediaTypeFile)
	}
	if uploadReq.RawSize != len(payload) {
		t.Fatalf("raw size = %d, want %d", uploadReq.RawSize, len(payload))
	}
	if len(encryptedUpload) == 0 || len(encryptedUpload)%16 != 0 {
		t.Fatalf("encrypted upload length = %d, want non-zero AES block multiple", len(encryptedUpload))
	}
	if len(sendReq.Msg.ItemList) != 1 {
		t.Fatalf("sent item count = %d, want 1", len(sendReq.Msg.ItemList))
	}
	item := sendReq.Msg.ItemList[0]
	if item.Type != wechatItemTypeFile || item.FileItem == nil {
		t.Fatalf("sent item = %#v, want file item", item)
	}
	if item.FileItem.FileName != "report.pdf" || item.FileItem.Len != "8" {
		t.Fatalf("file item name/len = %q/%q, want report.pdf/8", item.FileItem.FileName, item.FileItem.Len)
	}
	if item.FileItem.Media == nil || item.FileItem.Media.EncryptQueryParam != "download-token" {
		t.Fatalf("file media = %#v, want encrypted download token", item.FileItem.Media)
	}
}
