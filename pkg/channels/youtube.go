package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"

	"net/http"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const (
	youtubeAPIBase              = "https://www.googleapis.com/youtube/v3"
	youtubeMinPollInterval      = 5
	youtubeDefaultPollInterval  = 20
	youtubeRetryWaitSeconds     = 60
	youtubeHTTPTimeoutSeconds   = 10
	youtubeDefaultMessageFormat = "[YT] {author}: {message}"
	youtubeReconnectInterval    = 60 * time.Second
)

// YouTube Data API v3 response structures

type youtubeVideosResponse struct {
	Items []youtubeVideoItem `json:"items"`
}

type youtubeVideoItem struct {
	LiveStreamingDetails struct {
		ActiveLiveChatID string `json:"activeLiveChatId"`
	} `json:"liveStreamingDetails"`
}

type youtubeLiveChatResponse struct {
	NextPageToken     string                   `json:"nextPageToken"`
	PollingIntervalMs int                      `json:"pollingIntervalMillis"`
	Items             []youtubeLiveChatMessage `json:"items"`
	OfflineAt         string                   `json:"offlineAt,omitempty"`
	PageInfo          youtubePageInfo          `json:"pageInfo"`
	Error             *youtubeAPIError         `json:"error,omitempty"`
}

type youtubePageInfo struct {
	TotalResults   int `json:"totalResults"`
	ResultsPerPage int `json:"resultsPerPage"`
}

type youtubeLiveChatMessage struct {
	ID      string `json:"id"`
	Snippet struct {
		Type               string `json:"type"`
		LiveChatID         string `json:"liveChatId"`
		AuthorChannelID    string `json:"authorChannelId"`
		PublishedAt        string `json:"publishedAt"`
		HasDisplayContent  bool   `json:"hasDisplayContent"`
		DisplayMessage     string `json:"displayMessage"`
		TextMessageDetails *struct {
			MessageText string `json:"messageText"`
		} `json:"textMessageDetails,omitempty"`
		SuperChatDetails *struct {
			AmountMicros        string `json:"amountMicros"`
			Currency            string `json:"currency"`
			AmountDisplayString string `json:"amountDisplayString"`
			UserComment         string `json:"userComment"`
		} `json:"superChatDetails,omitempty"`
	} `json:"snippet"`
	AuthorDetails struct {
		ChannelID       string `json:"channelId"`
		ChannelURL      string `json:"channelUrl"`
		DisplayName     string `json:"displayName"`
		ProfileImageURL string `json:"profileImageUrl"`
		IsChatOwner     bool   `json:"isChatOwner"`
		IsChatSponsor   bool   `json:"isChatSponsor"`
		IsChatModerator bool   `json:"isChatModerator"`
	} `json:"authorDetails"`
}

type youtubeAPIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type youtubeSearchResponse struct {
	Items []youtubeSearchItem `json:"items"`
}

type youtubeSearchItem struct {
	ID struct {
		VideoID string `json:"videoId"`
	} `json:"id"`
	Snippet struct {
		Title string `json:"title"`
	} `json:"snippet"`
}

// YouTubeChannel implements the Channel interface for YouTube Live Chat.
type YouTubeChannel struct {
	*BaseChannel
	config          config.YouTubeConfig
	httpClient      *http.Client
	liveChatID      string
	nextPageToken   string
	cancel          context.CancelFunc
	reconnectCancel context.CancelFunc
}

func NewYouTubeChannel(cfg config.YouTubeConfig, msgBus *bus.MessageBus) (*YouTubeChannel, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("youtube: api_key is required")
	}
	if cfg.VideoID == "" && cfg.ChannelID == "" {
		return nil, fmt.Errorf("youtube: either video_id or channel_id is required")
	}

	pollInterval := cfg.PollIntervalSeconds
	if pollInterval < youtubeMinPollInterval {
		pollInterval = youtubeDefaultPollInterval
	}
	cfg.PollIntervalSeconds = pollInterval

	messageFormat := cfg.MessageFormat
	if messageFormat == "" {
		messageFormat = youtubeDefaultMessageFormat
		cfg.MessageFormat = messageFormat
	}

	base := NewBaseChannel("youtube", cfg, msgBus, cfg.AllowFrom)

	return &YouTubeChannel{
		BaseChannel: base,
		config:      cfg,
		httpClient: &http.Client{
			Timeout: youtubeHTTPTimeoutSeconds * time.Second,
		},
	}, nil
}

func (c *YouTubeChannel) Start(ctx context.Context) error {
	// If video_id is empty, resolve it from channel_id
	if c.config.VideoID == "" && c.config.ChannelID != "" {
		videoID, err := c.resolveVideoID()
		if err != nil {
			logger.WarnCF("youtube", "No active live stream found, will retry in background", map[string]any{
				"channel_id": c.config.ChannelID,
				"error":      err.Error(),
			})
			// Start reconnect loop to wait for a live stream
			reconnectCtx, reconnectCancel := context.WithCancel(ctx)
			c.reconnectCancel = reconnectCancel
			go c.reconnectLoop(reconnectCtx)
			return nil
		}
		c.config.VideoID = videoID
	}

	return c.connectToLiveChat(ctx)
}

func (c *YouTubeChannel) connectToLiveChat(ctx context.Context) error {
	liveChatID, err := c.fetchActiveLiveChatID()
	if err != nil {
		return fmt.Errorf("youtube: failed to get live chat ID: %w", err)
	}
	if liveChatID == "" {
		return fmt.Errorf("youtube: video %s is not currently live streaming", c.config.VideoID)
	}
	c.liveChatID = liveChatID
	c.nextPageToken = ""
	logger.InfoCF("youtube", "Connected to live chat", map[string]any{
		"video_id":     c.config.VideoID,
		"live_chat_id": liveChatID,
	})

	pollCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.setRunning(true)

	go c.pollLoop(pollCtx)

	return nil
}

func (c *YouTubeChannel) Stop(ctx context.Context) error {
	if c.reconnectCancel != nil {
		c.reconnectCancel()
		c.reconnectCancel = nil
	}
	if c.cancel != nil {
		c.cancel()
	}
	c.setRunning(false)
	logger.InfoC("youtube", "YouTube channel stopped")
	return nil
}

// resolveVideoID searches for an active live stream on the configured channel.
// Strategy: first try search.list with eventType=live (fast but may be cached),
// then fall back to searching recent videos and checking their liveStreamingDetails.
func (c *YouTubeChannel) resolveVideoID() (string, error) {
	// Strategy 1: search.list with eventType=live (may be cached by YouTube CDN)
	videoID, err := c.searchLiveStream()
	if err == nil {
		return videoID, nil
	}
	logger.DebugCF("youtube", "search.list eventType=live returned no results, trying fallback", map[string]any{
		"channel_id": c.config.ChannelID,
	})

	// Strategy 2: get recent videos and check if any are currently live
	return c.searchRecentVideosForLive()
}

// searchLiveStream uses search.list with eventType=live filter.
func (c *YouTubeChannel) searchLiveStream() (string, error) {
	url := fmt.Sprintf("%s/search?part=id,snippet&channelId=%s&eventType=live&type=video&key=%s",
		youtubeAPIBase, c.config.ChannelID, c.config.APIKey)

	resp, err := c.httpClient.Get(url)
	if err != nil {
		return "", fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("YouTube API returned status %d: %s", resp.StatusCode, string(body))
	}

	var searchResp youtubeSearchResponse
	if err := json.Unmarshal(body, &searchResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if len(searchResp.Items) == 0 {
		return "", fmt.Errorf("no results")
	}

	videoID := searchResp.Items[0].ID.VideoID
	title := searchResp.Items[0].Snippet.Title
	logger.InfoCF("youtube", "Auto-detected live stream (search)", map[string]any{
		"channel_id": c.config.ChannelID,
		"video_id":   videoID,
		"title":      title,
	})
	return videoID, nil
}

// searchRecentVideosForLive fetches recent videos from the channel and checks
// each one for an active live chat via videos.list. This avoids the search.list
// eventType=live cache delay issue.
func (c *YouTubeChannel) searchRecentVideosForLive() (string, error) {
	// Get recent videos (order=date, no eventType filter — not cached as heavily)
	url := fmt.Sprintf("%s/search?part=id&channelId=%s&type=video&order=date&maxResults=5&key=%s",
		youtubeAPIBase, c.config.ChannelID, c.config.APIKey)

	resp, err := c.httpClient.Get(url)
	if err != nil {
		return "", fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("YouTube API returned status %d: %s", resp.StatusCode, string(body))
	}

	var searchResp youtubeSearchResponse
	if err := json.Unmarshal(body, &searchResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if len(searchResp.Items) == 0 {
		return "", fmt.Errorf("no videos found for channel %s", c.config.ChannelID)
	}

	// Collect video IDs and batch-check via videos.list (1 quota unit)
	var ids []string
	for _, item := range searchResp.Items {
		ids = append(ids, item.ID.VideoID)
	}

	videosURL := fmt.Sprintf("%s/videos?part=liveStreamingDetails,snippet&id=%s&key=%s",
		youtubeAPIBase, strings.Join(ids, ","), c.config.APIKey)

	vResp, err := c.httpClient.Get(videosURL)
	if err != nil {
		return "", fmt.Errorf("videos.list request failed: %w", err)
	}
	defer vResp.Body.Close()

	vBody, err := io.ReadAll(vResp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read videos response: %w", err)
	}

	var videosResp struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title                string `json:"title"`
				LiveBroadcastContent string `json:"liveBroadcastContent"`
			} `json:"snippet"`
			LiveStreamingDetails struct {
				ActiveLiveChatID string `json:"activeLiveChatId"`
			} `json:"liveStreamingDetails"`
		} `json:"items"`
	}
	if err := json.Unmarshal(vBody, &videosResp); err != nil {
		return "", fmt.Errorf("failed to parse videos response: %w", err)
	}

	for _, v := range videosResp.Items {
		if v.LiveStreamingDetails.ActiveLiveChatID != "" {
			logger.InfoCF("youtube", "Auto-detected live stream (fallback)", map[string]any{
				"channel_id": c.config.ChannelID,
				"video_id":   v.ID,
				"title":      v.Snippet.Title,
			})
			return v.ID, nil
		}
	}

	return "", fmt.Errorf("no active live stream found for channel %s", c.config.ChannelID)
}

// reconnectLoop periodically searches for a new live stream when the current one ends.
func (c *YouTubeChannel) reconnectLoop(ctx context.Context) {
	ticker := time.NewTicker(youtubeReconnectInterval)
	defer ticker.Stop()

	logger.InfoCF("youtube", "Waiting for live stream", map[string]any{
		"channel_id":     c.config.ChannelID,
		"retry_interval": youtubeReconnectInterval.String(),
	})

	for {
		select {
		case <-ctx.Done():
			logger.InfoC("youtube", "Reconnect loop stopped")
			return
		case <-ticker.C:
			videoID, err := c.resolveVideoID()
			if err != nil {
				logger.DebugCF("youtube", "No live stream yet", map[string]any{
					"channel_id": c.config.ChannelID,
				})
				continue
			}
			c.config.VideoID = videoID
			if err := c.connectToLiveChat(ctx); err != nil {
				logger.ErrorCF("youtube", "Failed to connect to new live stream", map[string]any{
					"video_id": videoID,
					"error":    err.Error(),
				})
				continue
			}
			// Successfully connected, stop reconnect loop
			return
		}
	}
}

func (c *YouTubeChannel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	if c.config.ForwardChannel == "" || c.config.ForwardChatID == "" {
		logger.WarnC("youtube", "No forward channel configured, dropping response")
		return nil
	}
	c.bus.PublishOutbound(bus.OutboundMessage{
		Channel: c.config.ForwardChannel,
		ChatID:  c.config.ForwardChatID,
		Content: msg.Content,
	})
	return nil
}

// pollLoop continuously polls YouTube Live Chat for new messages.
func (c *YouTubeChannel) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(c.config.PollIntervalSeconds) * time.Second)
	defer ticker.Stop()

	// Do an initial poll immediately
	if streamEnded := c.pollOnce(); streamEnded {
		c.onStreamEnded(ctx)
		return
	}

	for {
		select {
		case <-ctx.Done():
			logger.InfoC("youtube", "Poll loop stopped (context cancelled)")
			return
		case <-ticker.C:
			if streamEnded := c.pollOnce(); streamEnded {
				c.onStreamEnded(ctx)
				return
			}
		}
	}
}

// onStreamEnded handles the transition when a live stream ends.
// If channel_id is configured, it starts the reconnect loop to find the next stream.
func (c *YouTubeChannel) onStreamEnded(ctx context.Context) {
	c.setRunning(false)
	if c.config.ChannelID != "" {
		logger.InfoCF("youtube", "Stream ended, will search for new stream", map[string]any{
			"channel_id": c.config.ChannelID,
		})
		c.config.VideoID = ""
		go c.reconnectLoop(ctx)
	} else {
		logger.WarnC("youtube", "Stream ended. Set channel_id in config to enable auto-reconnect.")
	}
}

func (c *YouTubeChannel) pollOnce() bool {
	resp, err := c.fetchLiveChatMessages()
	if err != nil {
		logger.ErrorCF("youtube", "Failed to fetch live chat messages", map[string]any{
			"error": err.Error(),
		})
		return false
	}

	if resp.Error != nil {
		return c.handleAPIError(resp.Error)
	}

	// Update page token for next poll
	if resp.NextPageToken != "" {
		c.nextPageToken = resp.NextPageToken
	}

	// Respect API's recommended polling interval
	if resp.PollingIntervalMs > 0 {
		recommendedSeconds := resp.PollingIntervalMs / 1000
		if recommendedSeconds > c.config.PollIntervalSeconds {
			logger.DebugCF("youtube", "API recommends longer poll interval", map[string]any{
				"recommended_ms": resp.PollingIntervalMs,
				"current_sec":    c.config.PollIntervalSeconds,
			})
		}
	}

	// Filter and process new messages
	filtered := c.preFilter(resp.Items)
	selected := c.selectComments(filtered)

	if c.config.BatchComments && len(selected) > 1 {
		c.batchAndHandle(selected)
	} else {
		for _, item := range selected {
			c.processMessage(item)
		}
	}
	return false
}

func (c *YouTubeChannel) processMessage(msg youtubeLiveChatMessage) {
	// Only process text messages
	if msg.Snippet.Type != "textMessageEvent" && msg.Snippet.Type != "superChatEvent" {
		return
	}

	authorName := msg.AuthorDetails.DisplayName
	authorChannelID := msg.AuthorDetails.ChannelID
	messageText := msg.Snippet.DisplayMessage

	if msg.Snippet.TextMessageDetails != nil {
		messageText = msg.Snippet.TextMessageDetails.MessageText
	}

	if messageText == "" {
		return
	}

	// Format message for forwarding
	formatted := c.formatMessage(authorName, messageText)

	metadata := map[string]string{
		"author_channel_id": authorChannelID,
		"author_name":       authorName,
		"message_id":        msg.ID,
		"published_at":      msg.Snippet.PublishedAt,
	}

	if msg.AuthorDetails.IsChatOwner {
		metadata["is_owner"] = "true"
	}
	if msg.AuthorDetails.IsChatModerator {
		metadata["is_moderator"] = "true"
	}
	if msg.Snippet.SuperChatDetails != nil {
		metadata["super_chat_amount"] = msg.Snippet.SuperChatDetails.AmountDisplayString
	}

	// Use authorChannelID as senderID, liveChatID as chatID
	c.HandleMessage(authorChannelID, c.liveChatID, formatted, nil, metadata)
}

func (c *YouTubeChannel) formatMessage(author, message string) string {
	formatted := c.config.MessageFormat
	formatted = strings.ReplaceAll(formatted, "{author}", author)
	formatted = strings.ReplaceAll(formatted, "{message}", message)
	return formatted
}

// handleAPIError logs the error and returns true if the stream has ended (triggering reconnect).
func (c *YouTubeChannel) handleAPIError(apiErr *youtubeAPIError) bool {
	switch apiErr.Code {
	case 401:
		logger.ErrorCF("youtube", "Authentication failed. Check your API key.", map[string]any{
			"code":    apiErr.Code,
			"message": apiErr.Message,
		})
	case 403:
		if strings.Contains(apiErr.Message, "quotaExceeded") || strings.Contains(apiErr.Message, "dailyLimitExceeded") {
			logger.ErrorCF("youtube", "API quota exceeded. Consider increasing poll_interval_seconds or requesting quota increase.", map[string]any{
				"code":    apiErr.Code,
				"message": apiErr.Message,
			})
		} else if strings.Contains(apiErr.Message, "forbidden") || strings.Contains(apiErr.Message, "liveChatDisabled") {
			logger.ErrorCF("youtube", "Access forbidden. liveChatMessages.list may require OAuth2 instead of API key.", map[string]any{
				"code":    apiErr.Code,
				"message": apiErr.Message,
			})
		} else if strings.Contains(apiErr.Message, "no longer live") || strings.Contains(apiErr.Message, "liveChatEnded") {
			logger.WarnCF("youtube", "Live stream has ended", map[string]any{
				"message": apiErr.Message,
			})
			return true
		} else {
			logger.ErrorCF("youtube", "API error (403)", map[string]any{
				"message": apiErr.Message,
			})
		}
	case 404:
		logger.WarnCF("youtube", "Live chat not found. The stream may have ended.", map[string]any{
			"code":    apiErr.Code,
			"message": apiErr.Message,
		})
		return true
	default:
		logger.ErrorCF("youtube", "YouTube API error", map[string]any{
			"code":    apiErr.Code,
			"message": apiErr.Message,
		})
	}
	return false
}

// fetchActiveLiveChatID retrieves the activeLiveChatId from a video's liveStreamingDetails.
func (c *YouTubeChannel) fetchActiveLiveChatID() (string, error) {
	url := fmt.Sprintf("%s/videos?part=liveStreamingDetails&id=%s&key=%s",
		youtubeAPIBase, c.config.VideoID, c.config.APIKey)

	resp, err := c.httpClient.Get(url)
	if err != nil {
		return "", fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("YouTube API returned status %d: %s", resp.StatusCode, string(body))
	}

	var videosResp youtubeVideosResponse
	if err := json.Unmarshal(body, &videosResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if len(videosResp.Items) == 0 {
		return "", fmt.Errorf("video not found: %s", c.config.VideoID)
	}

	return videosResp.Items[0].LiveStreamingDetails.ActiveLiveChatID, nil
}

// fetchLiveChatMessages retrieves live chat messages using the liveChatMessages.list endpoint.
func (c *YouTubeChannel) fetchLiveChatMessages() (*youtubeLiveChatResponse, error) {
	url := fmt.Sprintf("%s/liveChat/messages?liveChatId=%s&part=snippet,authorDetails&key=%s",
		youtubeAPIBase, c.liveChatID, c.config.APIKey)

	if c.nextPageToken != "" {
		url += "&pageToken=" + c.nextPageToken
	}

	resp, err := c.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var chatResp youtubeLiveChatResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// If HTTP status is not OK but response parsed, check for error field
	if resp.StatusCode != http.StatusOK {
		if chatResp.Error == nil {
			chatResp.Error = &youtubeAPIError{
				Code:    resp.StatusCode,
				Message: string(body),
			}
		}
	}

	return &chatResp, nil
}

// preFilter removes low-quality messages based on configured rules.
// Uses strings.Contains instead of regex for RPi ARM CPU optimization.
func (c *YouTubeChannel) preFilter(items []youtubeLiveChatMessage) []youtubeLiveChatMessage {
	if len(c.config.NGWords) == 0 && c.config.MinMessageLength == 0 &&
		c.config.MaxRepeatRatio == 0 && !c.config.BlockURLs {
		return items
	}

	filtered := make([]youtubeLiveChatMessage, 0, len(items))
	for _, item := range items {
		text := item.Snippet.DisplayMessage
		if item.Snippet.TextMessageDetails != nil {
			text = item.Snippet.TextMessageDetails.MessageText
		}
		if text == "" {
			continue
		}

		if c.shouldFilter(text) {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func (c *YouTubeChannel) shouldFilter(text string) bool {
	lower := strings.ToLower(text)

	for _, ng := range c.config.NGWords {
		if strings.Contains(lower, strings.ToLower(ng)) {
			return true
		}
	}

	if c.config.MinMessageLength > 0 {
		if len([]rune(text)) < c.config.MinMessageLength {
			return true
		}
	}

	if c.config.BlockURLs {
		if strings.Contains(text, "http://") || strings.Contains(text, "https://") {
			return true
		}
	}

	if c.config.MaxRepeatRatio > 0 {
		runes := []rune(text)
		if len(runes) > 0 {
			freq := make(map[rune]int)
			for _, r := range runes {
				freq[r]++
			}
			maxCount := 0
			for _, count := range freq {
				if count > maxCount {
					maxCount = count
				}
			}
			if float64(maxCount)/float64(len(runes)) > c.config.MaxRepeatRatio {
				return true
			}
		}
	}

	return false
}

// selectComments picks up to MaxCommentsPerPoll messages using the configured strategy.
func (c *YouTubeChannel) selectComments(msgs []youtubeLiveChatMessage) []youtubeLiveChatMessage {
	max := c.config.MaxCommentsPerPoll
	if max <= 0 || len(msgs) <= max {
		return msgs
	}

	switch c.config.SelectionStrategy {
	case "priority":
		prioritized := make([]youtubeLiveChatMessage, 0, len(msgs))
		normal := make([]youtubeLiveChatMessage, 0, len(msgs))
		for _, m := range msgs {
			if m.Snippet.SuperChatDetails != nil ||
				m.AuthorDetails.IsChatOwner ||
				m.AuthorDetails.IsChatModerator {
				prioritized = append(prioritized, m)
			} else {
				normal = append(normal, m)
			}
		}
		result := append(prioritized, normal...)
		if len(result) > max {
			result = result[:max]
		}
		return result
	case "random":
		shuffled := make([]youtubeLiveChatMessage, len(msgs))
		copy(shuffled, msgs)
		for i := len(shuffled) - 1; i > 0; i-- {
			j := rand.IntN(i + 1)
			shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
		}
		return shuffled[:max]
	default: // "latest"
		return msgs[len(msgs)-max:]
	}
}

// batchAndHandle combines multiple messages into a single batched inbound message.
// Uses bus.PublishInbound directly to bypass allowList check (preFiltered messages are safe).
func (c *YouTubeChannel) batchAndHandle(msgs []youtubeLiveChatMessage) {
	var sb strings.Builder
	sb.WriteString("[YouTube コメントまとめ]\n")
	for _, m := range msgs {
		author := m.AuthorDetails.DisplayName
		text := m.Snippet.DisplayMessage
		if m.Snippet.TextMessageDetails != nil {
			text = m.Snippet.TextMessageDetails.MessageText
		}
		fmt.Fprintf(&sb, "%s: %s\n", author, text)
	}
	sb.WriteString("---\n上記のコメントにまとめて応答してください。")

	c.bus.PublishInbound(bus.InboundMessage{
		Channel:  "youtube",
		SenderID: "youtube-batch",
		ChatID:   c.liveChatID,
		Content:  sb.String(),
		Metadata: map[string]string{
			"batch_size": fmt.Sprintf("%d", len(msgs)),
		},
	})
}
