package channels

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	YtChat "github.com/epjane/youtube-live-chat-downloader/v2"
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

	t.Run("missing api_key with data_api mode", func(t *testing.T) {
		cfg := config.YouTubeConfig{
			Enabled:    true,
			VideoID:    "test-video-id",
			ChatSource: "data_api",
		}
		_, err := NewYouTubeChannel(cfg, msgBus)
		if err == nil {
			t.Fatal("expected error for missing api_key in data_api mode")
		}
	})

	t.Run("missing api_key with innertube mode is OK", func(t *testing.T) {
		cfg := config.YouTubeConfig{
			Enabled:    true,
			VideoID:    "test-video-id",
			ChatSource: "innertube",
		}
		_, err := NewYouTubeChannel(cfg, msgBus)
		if err != nil {
			t.Fatalf("expected no error for innertube mode without api_key, got: %v", err)
		}
	})

	t.Run("missing video_id and channel_id", func(t *testing.T) {
		cfg := config.YouTubeConfig{
			Enabled: true,
			APIKey:  "test-api-key",
		}
		_, err := NewYouTubeChannel(cfg, msgBus)
		if err == nil {
			t.Fatal("expected error for missing video_id and channel_id")
		}
	})

	t.Run("channel_id only is valid", func(t *testing.T) {
		cfg := config.YouTubeConfig{
			Enabled:   true,
			APIKey:    "test-api-key",
			ChannelID: "UCxxxxxxxxxxxxxxxxxx",
		}
		ch, err := NewYouTubeChannel(cfg, msgBus)
		if err != nil {
			t.Fatalf("expected no error with channel_id only, got: %v", err)
		}
		if ch.config.ChannelID != "UCxxxxxxxxxxxxxxxxxx" {
			t.Errorf("expected channel_id 'UCxxxxxxxxxxxxxxxxxx', got '%s'", ch.config.ChannelID)
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
		err := ch.Send(context.TODO(), bus.OutboundMessage{
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

		err := ch.Send(context.TODO(), bus.OutboundMessage{
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

func TestYouTubeChannel_HandleAPIError(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cfg := config.YouTubeConfig{
		Enabled: true,
		APIKey:  "key",
		VideoID: "vid",
	}
	ch, _ := NewYouTubeChannel(cfg, msgBus)

	t.Run("403 no longer live returns true", func(t *testing.T) {
		err := &youtubeAPIError{Code: 403, Message: "The live chat is no longer live."}
		if !ch.handleAPIError(err) {
			t.Error("expected handleAPIError to return true for 'no longer live'")
		}
	})

	t.Run("403 liveChatEnded returns true", func(t *testing.T) {
		err := &youtubeAPIError{Code: 403, Message: "liveChatEnded"}
		if !ch.handleAPIError(err) {
			t.Error("expected handleAPIError to return true for 'liveChatEnded'")
		}
	})

	t.Run("404 returns true", func(t *testing.T) {
		err := &youtubeAPIError{Code: 404, Message: "not found"}
		if !ch.handleAPIError(err) {
			t.Error("expected handleAPIError to return true for 404")
		}
	})

	t.Run("403 quota exceeded returns false", func(t *testing.T) {
		err := &youtubeAPIError{Code: 403, Message: "quotaExceeded"}
		if ch.handleAPIError(err) {
			t.Error("expected handleAPIError to return false for quotaExceeded")
		}
	})

	t.Run("401 returns false", func(t *testing.T) {
		err := &youtubeAPIError{Code: 401, Message: "unauthorized"}
		if ch.handleAPIError(err) {
			t.Error("expected handleAPIError to return false for 401")
		}
	})
}

// makeTestMessages creates youtubeLiveChatMessage slice for testing.
func makeTestMessages(texts, authors []string) []youtubeLiveChatMessage {
	msgs := make([]youtubeLiveChatMessage, len(texts))
	for i := range texts {
		msgs[i].Snippet.DisplayMessage = texts[i]
		if i < len(authors) {
			msgs[i].AuthorDetails.DisplayName = authors[i]
			msgs[i].AuthorDetails.ChannelID = "UC" + authors[i]
		}
	}
	return msgs
}

func TestAccumulator_AppendAndFlush(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cfg := config.YouTubeConfig{
		Enabled:              true,
		APIKey:               "key",
		VideoID:              "vid",
		AccumulateComments:   true,
		MinAccumulateSeconds: 3,
		MaxAccumulateSeconds: 30,
		MaxCommentsPerPoll:   5,
		SelectionStrategy:    "latest",
		ForwardChannel:       "aituber",
		ForwardChatID:        "default",
		MessageFormat:        "[YT] {author}: {message}",
	}
	ch, err := NewYouTubeChannel(cfg, msgBus)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ch.liveChatID = "test-chat"

	msgs := makeTestMessages([]string{"hello", "world", "test"}, []string{"UserA", "UserB", "UserC"})

	ch.appendToBuffer(msgs)

	ch.bufferMu.Lock()
	if len(ch.commentBuffer) != 3 {
		t.Errorf("expected 3 buffered comments, got %d", len(ch.commentBuffer))
	}
	ch.bufferMu.Unlock()

	// Flush should process all buffered comments
	ch.flushCommentBuffer()

	ch.bufferMu.Lock()
	if len(ch.commentBuffer) != 0 {
		t.Errorf("expected empty buffer after flush, got %d", len(ch.commentBuffer))
	}
	ch.bufferMu.Unlock()
}

func TestAccumulator_SingleComment(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cfg := config.YouTubeConfig{
		Enabled:              true,
		APIKey:               "key",
		VideoID:              "vid",
		AccumulateComments:   true,
		MinAccumulateSeconds: 3,
		MaxAccumulateSeconds: 30,
		MaxCommentsPerPoll:   5,
		SelectionStrategy:    "latest",
		ForwardChannel:       "aituber",
		ForwardChatID:        "default",
		MessageFormat:        "[YT] {author}: {message}",
	}
	ch, err := NewYouTubeChannel(cfg, msgBus)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ch.liveChatID = "test-chat"

	// Buffer a single comment
	msgs := makeTestMessages([]string{"solo"}, []string{"UserA"})
	ch.appendToBuffer(msgs)

	// Flush — single comment should use processMessage (not batchAndHandle)
	ch.flushCommentBuffer()

	ch.bufferMu.Lock()
	if len(ch.commentBuffer) != 0 {
		t.Errorf("expected empty buffer after flush, got %d", len(ch.commentBuffer))
	}
	ch.bufferMu.Unlock()
}

func TestAccumulator_DiscardOnStreamEnd(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cfg := config.YouTubeConfig{
		Enabled:              true,
		APIKey:               "key",
		VideoID:              "vid",
		AccumulateComments:   true,
		MinAccumulateSeconds: 3,
		MaxAccumulateSeconds: 30,
	}
	ch, err := NewYouTubeChannel(cfg, msgBus)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msgs := makeTestMessages([]string{"hello"}, []string{"UserA"})
	ch.appendToBuffer(msgs)

	ch.discardBuffer()

	ch.bufferMu.Lock()
	if len(ch.commentBuffer) != 0 {
		t.Errorf("expected empty buffer after discard, got %d", len(ch.commentBuffer))
	}
	ch.bufferMu.Unlock()
}

func TestAccumulator_DisabledByDefault(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cfg := config.YouTubeConfig{
		Enabled: true,
		APIKey:  "key",
		VideoID: "vid",
	}
	ch, err := NewYouTubeChannel(cfg, msgBus)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ch.config.AccumulateComments {
		t.Error("expected AccumulateComments to be false by default")
	}
	if ch.commentNotify != nil {
		t.Error("expected commentNotify to be nil when accumulate is disabled")
	}
}

func TestAccumulator_DefaultsApplied(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cfg := config.YouTubeConfig{
		Enabled:            true,
		APIKey:             "key",
		VideoID:            "vid",
		AccumulateComments: true,
	}
	ch, err := NewYouTubeChannel(cfg, msgBus)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ch.config.MinAccumulateSeconds != youtubeDefaultMinAccumulate {
		t.Errorf("expected MinAccumulateSeconds=%d, got %d", youtubeDefaultMinAccumulate, ch.config.MinAccumulateSeconds)
	}
	if ch.config.MaxAccumulateSeconds != youtubeDefaultMaxAccumulate {
		t.Errorf("expected MaxAccumulateSeconds=%d, got %d", youtubeDefaultMaxAccumulate, ch.config.MaxAccumulateSeconds)
	}
	if ch.commentNotify == nil {
		t.Error("expected commentNotify to be initialized when accumulate is enabled")
	}
}

// ─────────────────────────────────────────────────────────────
// InnerTube hybrid tests
// ─────────────────────────────────────────────────────────────

func TestConvertInnerTubeMessages(t *testing.T) {
	now := time.Now()
	input := []YtChat.ChatMessage{
		{AuthorName: "User1", Message: "Hello", Timestamp: now},
		{AuthorName: "User2", Message: "", Timestamp: now},
		{AuthorName: "User3", Message: "World", Timestamp: now},
	}
	result := convertInnerTubeMessages(input)

	if len(result) != 2 {
		t.Fatalf("expected 2 messages (empty filtered), got %d", len(result))
	}

	if result[0].AuthorDetails.DisplayName != "User1" {
		t.Errorf("expected author 'User1', got '%s'", result[0].AuthorDetails.DisplayName)
	}
	if result[0].Snippet.Type != "textMessageEvent" {
		t.Errorf("expected type 'textMessageEvent', got '%s'", result[0].Snippet.Type)
	}
	if result[0].Snippet.DisplayMessage != "Hello" {
		t.Errorf("expected message 'Hello', got '%s'", result[0].Snippet.DisplayMessage)
	}
	if result[0].Snippet.TextMessageDetails == nil || result[0].Snippet.TextMessageDetails.MessageText != "Hello" {
		t.Error("expected TextMessageDetails.MessageText to be 'Hello'")
	}
	if result[0].Snippet.PublishedAt != now.Format(time.RFC3339) {
		t.Errorf("expected PublishedAt '%s', got '%s'", now.Format(time.RFC3339), result[0].Snippet.PublishedAt)
	}

	if result[1].AuthorDetails.DisplayName != "User3" {
		t.Errorf("expected second author 'User3', got '%s'", result[1].AuthorDetails.DisplayName)
	}
}

func TestConvertInnerTubeMessages_Empty(t *testing.T) {
	result := convertInnerTubeMessages(nil)
	if len(result) != 0 {
		t.Errorf("expected 0 messages for nil input, got %d", len(result))
	}

	result = convertInnerTubeMessages([]YtChat.ChatMessage{})
	if len(result) != 0 {
		t.Errorf("expected 0 messages for empty input, got %d", len(result))
	}
}

func TestFetchInnerTubeChat_ContextCancel(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cfg := config.YouTubeConfig{
		Enabled:    true,
		VideoID:    "test-video-id",
		ChatSource: "innertube",
	}
	ch, err := NewYouTubeChannel(cfg, msgBus)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err = ch.fetchInnerTubeChat(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestNewYouTubeChannel_ChatSourceDefault(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cfg := config.YouTubeConfig{
		Enabled: true,
		APIKey:  "key",
		VideoID: "vid",
	}
	ch, err := NewYouTubeChannel(cfg, msgBus)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ch.config.ChatSource != "innertube" {
		t.Errorf("expected default ChatSource 'innertube', got '%s'", ch.config.ChatSource)
	}
}
