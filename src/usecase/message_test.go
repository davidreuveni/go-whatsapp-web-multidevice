package usecase

import (
	"testing"

	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
)

func TestBuildForwardMessageProtoForwardedContext(t *testing.T) {
	storedMessage := &domainChatStorage.Message{
		ID:      "3EB0789ABC123456",
		Content: "hello",
	}

	for _, tt := range []struct {
		name              string
		markAsForwarded   bool
		wantContextInfo   bool
		wantForwardedFlag bool
	}{
		{
			name:              "marks forwarded",
			markAsForwarded:   true,
			wantContextInfo:   true,
			wantForwardedFlag: true,
		},
		{
			name:            "does not mark forwarded",
			markAsForwarded: false,
			wantContextInfo: false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			msg, _, err := buildForwardMessageProto(storedMessage, tt.markAsForwarded, nil)
			if err != nil {
				t.Fatalf("buildForwardMessageProto() error = %v", err)
			}

			ctxInfo := msg.GetExtendedTextMessage().GetContextInfo()
			if gotContextInfo := ctxInfo != nil; gotContextInfo != tt.wantContextInfo {
				t.Fatalf("contextInfo presence = %t, want %t", gotContextInfo, tt.wantContextInfo)
			}

			if tt.wantContextInfo && ctxInfo.GetIsForwarded() != tt.wantForwardedFlag {
				t.Fatalf("isForwarded = %t, want %t", ctxInfo.GetIsForwarded(), tt.wantForwardedFlag)
			}
		})
	}
}

func TestBuildForwardMessageProtoStripsLegacySentVideoPrefix(t *testing.T) {
	storedMessage := &domainChatStorage.Message{
		ID:            "3EB0789ABC123456",
		Content:       "🎥 test video forward",
		IsFromMe:      true,
		MediaType:     "video",
		URL:           "https://mmg.whatsapp.net/test",
		MediaKey:      []byte("media-key"),
		FileSHA256:    []byte("file-sha"),
		FileEncSHA256: []byte("file-enc-sha"),
		FileLength:    123,
	}

	msg, content, err := buildForwardMessageProto(storedMessage, false, nil)
	if err != nil {
		t.Fatalf("buildForwardMessageProto() error = %v", err)
	}

	if content != "test video forward" {
		t.Fatalf("stored forwarded content = %q, want %q", content, "test video forward")
	}
	if got := msg.GetVideoMessage().GetCaption(); got != "test video forward" {
		t.Fatalf("video caption = %q, want %q", got, "test video forward")
	}
}

func TestBuildForwardMessageProtoStripsLegacySentVideoDefaultCaption(t *testing.T) {
	storedMessage := &domainChatStorage.Message{
		ID:            "3EB0789ABC123456",
		Content:       "🎥 Video",
		IsFromMe:      true,
		MediaType:     "video",
		URL:           "https://mmg.whatsapp.net/test",
		MediaKey:      []byte("media-key"),
		FileSHA256:    []byte("file-sha"),
		FileEncSHA256: []byte("file-enc-sha"),
		FileLength:    123,
	}

	msg, content, err := buildForwardMessageProto(storedMessage, false, nil)
	if err != nil {
		t.Fatalf("buildForwardMessageProto() error = %v", err)
	}

	if content != "" {
		t.Fatalf("stored forwarded content = %q, want empty", content)
	}
	if got := msg.GetVideoMessage().GetCaption(); got != "" {
		t.Fatalf("video caption = %q, want empty", got)
	}
}
