package channels

import (
	"encoding/json"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestNewYouTubeChannel(t *testing.T) {
	msgBus := bus.NewMessageBus()

	t.Run("valid config", func(t *testing.T) {
		cfg := config.YouTubeConfig{
			Enabled:             true,
			APIKey:              "test-api-key",
			VideoID:             "test-video-id",
			PollIntervalSeconds: 30,
			ForwardChannel:      "discord",
			ForwardChatID:       "123456",
			MessageFormat:       "[YT] {author}: {message}",
		}
		ch, err := NewYouTubeChannel(cfg, msgBus)
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if ch.Name() != "youtube" {
			t.Errorf("expected name 'youtube', got '%s'", ch.Name())
		}
	})

	t.Run("missing api_key", func(t *testing.T) {
		cfg := config.YouTubeConfig{
			Enabled: true,
			VideoID: "test-video-id",
		}
		_, err := NewYouTubeChannel(cfg, msgBus)
		if err == nil {
			t.Fatal("expected error for missing api_key")
		}
	})

	t.Run("missing video_id", func(t *testing.T) {
		cfg := config.YouTubeConfig{
			Enabled: true,
			APIKey:  "test-api-key",
		}
		_, err := NewYouTubeChannel(cfg, msgBus)
		if err == nil {
			t.Fatal("expected error for missing video_id")
		}
	})

	t.Run("poll interval clamped to minimum", func(t *testing.T) {
		cfg := config.YouTubeConfig{
			Enabled:             true,
			APIKey:              "test-api-key",
			VideoID:             "test-video-id",
			PollIntervalSeconds: 2, // below minimum
		}
		ch, err := NewYouTubeChannel(cfg, msgBus)
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if ch.config.PollIntervalSeconds < youtubeMinPollInterval {
			t.Errorf("expected poll interval >= %d, got %d", youtubeMinPollInterval, ch.config.PollIntervalSeconds)
		}
	})

	t.Run("default message format applied", func(t *testing.T) {
		cfg := config.YouTubeConfig{
			Enabled: true,
			APIKey:  "test-api-key",
			VideoID: "test-video-id",
		}
		ch, err := NewYouTubeChannel(cfg, msgBus)
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if ch.config.MessageFormat != youtubeDefaultMessageFormat {
			t.Errorf("expected default message format '%s', got '%s'", youtubeDefaultMessageFormat, ch.config.MessageFormat)
		}
	})
}

func TestYouTubeChannel_IsAllowed(t *testing.T) {
	msgBus := bus.NewMessageBus()

	t.Run("empty allowlist allows all", func(t *testing.T) {
		cfg := config.YouTubeConfig{
			Enabled:   true,
			APIKey:    "key",
			VideoID:   "vid",
			AllowFrom: config.FlexibleStringSlice{},
		}
		ch, _ := NewYouTubeChannel(cfg, msgBus)
		if !ch.IsAllowed("any-user") {
			t.Error("expected any user to be allowed with empty allowlist")
		}
	})

	t.Run("allowlist filters users", func(t *testing.T) {
		cfg := config.YouTubeConfig{
			Enabled:   true,
			APIKey:    "key",
			VideoID:   "vid",
			AllowFrom: config.FlexibleStringSlice{"UCxxx123"},
		}
		ch, _ := NewYouTubeChannel(cfg, msgBus)
		if !ch.IsAllowed("UCxxx123") {
			t.Error("expected allowed user to pass")
		}
		if ch.IsAllowed("UCother456") {
			t.Error("expected non-allowed user to be rejected")
		}
	})
}

func TestYouTubeChannel_Send(t *testing.T) {
	msgBus := bus.NewMessageBus()

	t.Run("forwards to configured channel", func(t *testing.T) {
		cfg := config.YouTubeConfig{
			Enabled:        true,
			APIKey:         "key",
			VideoID:        "vid",
			ForwardChannel: "discord",
			ForwardChatID:  "999",
		}
		ch, _ := NewYouTubeChannel(cfg, msgBus)

		// Send a message — it should be forwarded via bus
		err := ch.Send(nil, bus.OutboundMessage{
			Channel: "youtube",
			ChatID:  "livechat123",
			Content: "Hello from AI",
		})
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	})

	t.Run("no forward channel configured", func(t *testing.T) {
		cfg := config.YouTubeConfig{
			Enabled: true,
			APIKey:  "key",
			VideoID: "vid",
		}
		ch, _ := NewYouTubeChannel(cfg, msgBus)

		err := ch.Send(nil, bus.OutboundMessage{
			Channel: "youtube",
			Content: "Hello",
		})
		if err != nil {
			t.Fatalf("expected no error even without forward channel, got: %v", err)
		}
	})
}

func TestYouTubeChannel_FormatMessage(t *testing.T) {
	msgBus := bus.NewMessageBus()

	tests := []struct {
		name     string
		format   string
		author   string
		message  string
		expected string
	}{
		{
			name:     "default format",
			format:   "[YT] {author}: {message}",
			author:   "TestUser",
			message:  "Hello world",
			expected: "[YT] TestUser: Hello world",
		},
		{
			name:     "custom format",
			format:   "YouTube | {author} said: {message}",
			author:   "Streamer",
			message:  "Great stream!",
			expected: "YouTube | Streamer said: Great stream!",
		},
		{
			name:     "message only format",
			format:   "{message} (by {author})",
			author:   "User1",
			message:  "Test",
			expected: "Test (by User1)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.YouTubeConfig{
				Enabled:       true,
				APIKey:        "key",
				VideoID:       "vid",
				MessageFormat: tt.format,
			}
			ch, _ := NewYouTubeChannel(cfg, msgBus)
			result := ch.formatMessage(tt.author, tt.message)
			if result != tt.expected {
				t.Errorf("expected '%s', got '%s'", tt.expected, result)
			}
		})
	}
}

func TestParseLiveChatResponse(t *testing.T) {
	raw := `{
		"nextPageToken": "abc123",
		"pollingIntervalMillis": 10000,
		"items": [
			{
				"id": "msg1",
				"snippet": {
					"type": "textMessageEvent",
					"liveChatId": "chat123",
					"authorChannelId": "UC123",
					"publishedAt": "2026-02-26T12:00:00Z",
					"hasDisplayContent": true,
					"displayMessage": "Hello!",
					"textMessageDetails": {
						"messageText": "Hello!"
					}
				},
				"authorDetails": {
					"channelId": "UC123",
					"displayName": "TestUser",
					"isChatOwner": false,
					"isChatModerator": false
				}
			}
		],
		"pageInfo": {
			"totalResults": 1,
			"resultsPerPage": 200
		}
	}`

	var resp youtubeLiveChatResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp.NextPageToken != "abc123" {
		t.Errorf("expected nextPageToken 'abc123', got '%s'", resp.NextPageToken)
	}
	if resp.PollingIntervalMs != 10000 {
		t.Errorf("expected pollingIntervalMillis 10000, got %d", resp.PollingIntervalMs)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(resp.Items))
	}

	item := resp.Items[0]
	if item.ID != "msg1" {
		t.Errorf("expected ID 'msg1', got '%s'", item.ID)
	}
	if item.Snippet.Type != "textMessageEvent" {
		t.Errorf("expected type 'textMessageEvent', got '%s'", item.Snippet.Type)
	}
	if item.AuthorDetails.DisplayName != "TestUser" {
		t.Errorf("expected displayName 'TestUser', got '%s'", item.AuthorDetails.DisplayName)
	}
	if item.Snippet.TextMessageDetails == nil || item.Snippet.TextMessageDetails.MessageText != "Hello!" {
		t.Error("expected textMessageDetails.messageText to be 'Hello!'")
	}
}
