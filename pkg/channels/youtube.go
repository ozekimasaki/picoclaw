package channels

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	YtChat "github.com/epjane/youtube-live-chat-downloader/v2"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const (
	youtubeAPIBase                = "https://www.googleapis.com/youtube/v3"
	youtubeMinPollInterval        = 5
	youtubeDefaultPollInterval    = 20
	youtubeRetryWaitSeconds       = 60
	youtubeHTTPTimeoutSeconds     = 10
	youtubeDefaultMessageFormat   = "[YT] {author}: {message}"
	youtubeReconnectInterval      = 60 * time.Second
	youtubeMaxReconnectInterval   = 5 * time.Minute
	youtubeRSSFeedBase            = "https://www.youtube.com/feeds/videos.xml?channel_id="
	youtubeDefaultMinAccumulate   = 3  // seconds
	youtubeDefaultMaxAccumulate   = 30 // seconds
	innerTubeMaxConsecutiveErrors = 5
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

// RSS feed structures (YouTube channel feed — 0 API quota)
type youtubeRSSFeed struct {
	XMLName xml.Name          `xml:"feed"`
	Entries []youtubeRSSEntry `xml:"entry"`
}

type youtubeRSSEntry struct {
	VideoID   string `xml:"videoId"`
	Title     string `xml:"title"`
	Published string `xml:"published"`
}

// YouTubeChannel implements the Channel interface for YouTube Live Chat.
type YouTubeChannel struct {
	*BaseChannel
	config             config.YouTubeConfig
	httpClient         *http.Client
	liveChatID         string
	nextPageToken      string
	cancel             context.CancelFunc
	reconnectCancel    context.CancelFunc
	parentCtx          context.Context
	commentBuffer      []youtubeLiveChatMessage
	bufferMu           sync.Mutex
	commentNotify      chan struct{}
	ttsReady           <-chan struct{}
	innerContinuation  string
	innerCfg           YtChat.YtCfg
	superChatPageToken string
}

func NewYouTubeChannel(cfg config.YouTubeConfig, msgBus *bus.MessageBus) (*YouTubeChannel, error) {
	if cfg.ChatSource == "" {
		cfg.ChatSource = "innertube"
	}
	if cfg.ChatSource == "data_api" && cfg.APIKey == "" {
		return nil, fmt.Errorf("youtube: api_key is required for chat_source=data_api")
	}
	if cfg.VideoID == "" && cfg.ChannelID == "" {
		return nil, fmt.Errorf("youtube: either video_id or channel_id is required")
	}
	if cfg.APIKey == "" {
		logger.WarnC("youtube", "api_key not set: no search.list fallback, no SuperChat polling")
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

	if cfg.AccumulateComments {
		if cfg.MinAccumulateSeconds <= 0 {
			cfg.MinAccumulateSeconds = youtubeDefaultMinAccumulate
		}
		if cfg.MaxAccumulateSeconds <= 0 {
			cfg.MaxAccumulateSeconds = youtubeDefaultMaxAccumulate
		}
	}

	base := NewBaseChannel("youtube", cfg, msgBus, cfg.AllowFrom)

	ch := &YouTubeChannel{
		BaseChannel: base,
		config:      cfg,
		httpClient: &http.Client{
			Timeout: youtubeHTTPTimeoutSeconds * time.Second,
		},
	}
	if cfg.AccumulateComments {
		ch.commentNotify = make(chan struct{}, 1)
	}
	return ch, nil
}

func (c *YouTubeChannel) Start(ctx context.Context) error {
	c.parentCtx = ctx
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
	if c.config.ChatSource == "innertube" {
		// ── InnerTube initialization (0 units) ──
		if err := c.initInnerTube(); err != nil {
			logger.WarnCF("youtube", "InnerTube init failed, trying Data API fallback", map[string]any{
				"error": err.Error(),
			})
			c.fallbackToDataAPI(ctx)
			return nil
		}

		// ── SuperChat liveChatID (api_key required, 1 unit) ──
		if c.config.APIKey != "" && c.config.SuperchatPollSeconds > 0 {
			if c.config.LiveChatID != "" {
				c.liveChatID = c.config.LiveChatID
			} else {
				liveChatID, err := c.fetchActiveLiveChatID()
				if err != nil {
					logger.WarnCF("youtube", "Could not get liveChatID for SuperChat polling", map[string]any{
						"error": err.Error(),
					})
				} else {
					c.liveChatID = liveChatID
				}
			}
		}

		logger.InfoCF("youtube", "Connected via InnerTube", map[string]any{
			"video_id":       c.config.VideoID,
			"superchat_poll": c.liveChatID != "" && c.config.SuperchatPollSeconds > 0,
		})

		pollCtx, cancel := context.WithCancel(ctx)
		c.cancel = cancel
		c.setRunning(true)

		go c.innerTubePollLoop(pollCtx)
		if c.liveChatID != "" && c.config.SuperchatPollSeconds > 0 {
			go c.superChatPollLoop(pollCtx)
		}
		if c.config.AccumulateComments {
			go c.flushLoop(pollCtx)
		}
		return nil
	}

	// ── data_api mode: existing logic ──
	if c.config.LiveChatID != "" {
		c.liveChatID = c.config.LiveChatID
		logger.InfoCF("youtube", "Using directly configured live_chat_id", map[string]any{
			"live_chat_id": c.liveChatID,
		})
	} else {
		liveChatID, err := c.fetchActiveLiveChatID()
		if err != nil {
			return fmt.Errorf("youtube: failed to get live chat ID: %w", err)
		}
		if liveChatID == "" {
			return fmt.Errorf("youtube: video %s is not currently live streaming", c.config.VideoID)
		}
		c.liveChatID = liveChatID
	}
	c.nextPageToken = ""
	logger.InfoCF("youtube", "Connected to live chat", map[string]any{
		"video_id":     c.config.VideoID,
		"live_chat_id": c.liveChatID,
	})

	pollCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.setRunning(true)

	go c.pollLoop(pollCtx)
	if c.config.AccumulateComments {
		go c.flushLoop(pollCtx)
	}

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
// Strategy: RSS feed (0 quota) → videos.list (1 unit) to check live status.
// Falls back to search.list only if RSS yields no candidates.
func (c *YouTubeChannel) resolveVideoID() (string, error) {
	// Strategy 1: RSS feed (0 API quota) + videos.list (1 unit)
	videoID, err := c.resolveViaRSS()
	if err == nil {
		return videoID, nil
	}
	logger.DebugCF("youtube", "RSS-based detection found no live stream", map[string]any{
		"channel_id": c.config.ChannelID,
		"error":      err.Error(),
	})

	// Strategy 2: search.list as last resort (100 units — only if RSS fails)
	videoID, err = c.searchLiveStream()
	if err == nil {
		return videoID, nil
	}

	return "", fmt.Errorf("no active live stream found for channel %s", c.config.ChannelID)
}

// resolveViaRSS fetches the channel's RSS feed (0 API quota) to get recent video IDs,
// then checks them via videos.list (1 API unit) to find an active live stream.
func (c *YouTubeChannel) resolveViaRSS() (string, error) {
	feedURL := youtubeRSSFeedBase + c.config.ChannelID
	resp, err := c.httpClient.Get(feedURL)
	if err != nil {
		return "", fmt.Errorf("RSS fetch failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("RSS read failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("RSS returned status %d", resp.StatusCode)
	}

	var feed youtubeRSSFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return "", fmt.Errorf("RSS parse failed: %w", err)
	}

	if len(feed.Entries) == 0 {
		return "", fmt.Errorf("RSS feed empty for channel %s", c.config.ChannelID)
	}

	// Take up to 5 most recent video IDs from RSS
	var ids []string
	for i, entry := range feed.Entries {
		if i >= 5 {
			break
		}
		ids = append(ids, entry.VideoID)
	}

	logger.DebugCF("youtube", "RSS feed fetched, checking videos for live status", map[string]any{
		"channel_id":    c.config.ChannelID,
		"candidate_ids": strings.Join(ids, ","),
	})

	// Batch check via videos.list (1 API unit total)
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

	if vResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("videos.list returned status %d: %s", vResp.StatusCode, string(vBody))
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
			logger.InfoCF("youtube", "Auto-detected live stream via RSS+API", map[string]any{
				"channel_id": c.config.ChannelID,
				"video_id":   v.ID,
				"title":      v.Snippet.Title,
			})
			return v.ID, nil
		}
	}

	return "", fmt.Errorf("no active live stream in recent videos")
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
// Uses exponential backoff: starts at youtubeReconnectInterval, doubles up to youtubeMaxReconnectInterval.
// RSS-based detection uses 0 API quota; search.list fallback is rate-limited.
func (c *YouTubeChannel) reconnectLoop(ctx context.Context) {
	interval := youtubeReconnectInterval
	rssOnly := false // after first search.list fallback, switch to RSS-only to save quota

	logger.InfoCF("youtube", "Waiting for live stream", map[string]any{
		"channel_id":     c.config.ChannelID,
		"retry_interval": interval.String(),
	})

	for {
		select {
		case <-ctx.Done():
			logger.InfoC("youtube", "Reconnect loop stopped")
			return
		case <-time.After(interval):
		}

		var videoID string
		var err error

		if rssOnly {
			// RSS-only mode: 0 API quota per attempt
			videoID, err = c.resolveViaRSS()
		} else {
			// Full resolution: RSS first, then search.list fallback
			videoID, err = c.resolveVideoID()
			if err != nil {
				// After first full attempt, switch to RSS-only to preserve quota
				rssOnly = true
				logger.InfoCF("youtube", "Switching to RSS-only detection to preserve API quota", map[string]any{
					"channel_id": c.config.ChannelID,
				})
			}
		}

		if err != nil {
			logger.DebugCF("youtube", "No live stream yet", map[string]any{
				"channel_id":    c.config.ChannelID,
				"next_interval": interval.String(),
				"rss_only":      rssOnly,
			})
			// Exponential backoff: double interval up to max
			if interval < youtubeMaxReconnectInterval {
				interval *= 2
				if interval > youtubeMaxReconnectInterval {
					interval = youtubeMaxReconnectInterval
				}
			}
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
// Uses parentCtx (not pollCtx) for reconnect to avoid goroutine leaks.
func (c *YouTubeChannel) onStreamEnded(ctx context.Context) {
	c.discardBuffer()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.setRunning(false)
	if c.config.ChannelID != "" {
		logger.InfoCF("youtube", "Stream ended, will search for new stream", map[string]any{
			"channel_id": c.config.ChannelID,
		})
		c.config.VideoID = ""
		go c.reconnectLoop(c.parentCtx)
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

	// Accumulate mode: buffer comments for TTS-synchronized batch processing
	if c.config.AccumulateComments && len(filtered) > 0 {
		c.appendToBuffer(filtered)
		return false
	}

	// Standard mode: process immediately
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

// appendToBuffer adds pre-filtered comments to the accumulation buffer.
func (c *YouTubeChannel) appendToBuffer(msgs []youtubeLiveChatMessage) {
	c.bufferMu.Lock()
	c.commentBuffer = append(c.commentBuffer, msgs...)
	count := len(c.commentBuffer)
	c.bufferMu.Unlock()

	logger.DebugCF("youtube", "Comments buffered", map[string]any{
		"added": len(msgs), "total": count,
	})

	select {
	case c.commentNotify <- struct{}{}:
	default:
	}
}

// flushLoop manages TTS-synchronized comment batch processing.
func (c *YouTubeChannel) flushLoop(ctx context.Context) {
	minWait := time.Duration(c.config.MinAccumulateSeconds) * time.Second
	maxWait := time.Duration(c.config.MaxAccumulateSeconds) * time.Second

	logger.InfoCF("youtube", "Comment accumulator started", map[string]any{
		"min_wait": minWait.String(), "max_wait": maxWait.String(),
		"has_tts_signal": c.ttsReady != nil,
	})

	for {
		// Phase 1: Wait for first comment
		select {
		case <-ctx.Done():
			return
		case <-c.commentNotify:
		}

		// Phase 2: Minimum accumulation time
		select {
		case <-ctx.Done():
			return
		case <-time.After(minWait):
		}

		// Phase 3: Wait for TTS completion or max timeout
		if c.ttsReady != nil {
			remaining := maxWait - minWait
			if remaining > 0 {
				select {
				case <-ctx.Done():
					return
				case <-c.ttsReady:
				case <-time.After(remaining):
					logger.DebugC("youtube", "Max accumulate timeout, forcing flush")
				}
			}
		}

		// Phase 4: Flush
		c.flushCommentBuffer()
	}
}

// flushCommentBuffer processes all accumulated comments as a single batch.
func (c *YouTubeChannel) flushCommentBuffer() {
	c.bufferMu.Lock()
	if len(c.commentBuffer) == 0 {
		c.bufferMu.Unlock()
		return
	}
	comments := c.commentBuffer
	c.commentBuffer = nil
	c.bufferMu.Unlock()

	selected := c.selectComments(comments)
	if len(selected) == 0 {
		return
	}

	logger.InfoCF("youtube", "Flushing accumulated comments", map[string]any{
		"total_buffered": len(comments), "selected": len(selected),
	})

	if len(selected) == 1 {
		c.processMessage(selected[0])
	} else {
		c.batchAndHandle(selected)
	}
}

// discardBuffer clears the comment buffer (called when stream ends).
func (c *YouTubeChannel) discardBuffer() {
	c.bufferMu.Lock()
	n := len(c.commentBuffer)
	c.commentBuffer = nil
	c.bufferMu.Unlock()
	if n > 0 {
		logger.InfoCF("youtube", "Discarded comment buffer", map[string]any{"count": n})
	}
}

// SetTTSReady sets the TTS completion signal channel from AITuber.
func (c *YouTubeChannel) SetTTSReady(ch <-chan struct{}) {
	c.ttsReady = ch
}

// ─────────────────────────────────────────────────────────────
// InnerTube hybrid chat acquisition
// ─────────────────────────────────────────────────────────────

// initInnerTube initializes InnerTube connection with retry and backoff.
// Parses the YouTube watch page HTML to extract continuation token and InnerTube context.
func (c *YouTubeChannel) initInnerTube() error {
	videoURL := fmt.Sprintf("https://www.youtube.com/watch?v=%s", c.config.VideoID)

	// Cookie setup for bot-detection prevention (R2)
	YtChat.AddCookies([]*http.Cookie{
		{Name: "PREF", Value: "tz=Asia/Tokyo", MaxAge: 86400},
		{Name: "CONSENT", Value: fmt.Sprintf("YES+yt.432048971.ja+FX+%d", 100+rand.IntN(900)), MaxAge: 86400},
	})

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		cont, cfg, err := YtChat.ParseInitialData(videoURL)
		if err == nil {
			c.innerContinuation = cont
			c.innerCfg = cfg
			logger.InfoCF("youtube", "InnerTube initialized", map[string]any{
				"video_id": c.config.VideoID,
				"attempt":  attempt + 1,
			})
			return nil
		}

		lastErr = err
		if errors.Is(err, YtChat.ErrStreamNotLive) {
			return fmt.Errorf("stream not live: %w", err)
		}

		wait := time.Duration(1<<uint(attempt)) * 10 * time.Second // 10s, 20s, 40s
		logger.WarnCF("youtube", "InnerTube init failed, retrying", map[string]any{
			"attempt": attempt + 1,
			"wait":    wait.String(),
			"error":   err.Error(),
		})
		time.Sleep(wait)
	}
	return fmt.Errorf("innertube init failed after 3 attempts: %w", lastErr)
}

// fetchInnerTubeChat wraps FetchContinuationChat with context cancellation.
// FetchContinuationChat internally calls time.Sleep(timeoutMs), blocking the goroutine.
// This wrapper runs it in a sub-goroutine and uses select to respond to ctx.Done() promptly.
func (c *YouTubeChannel) fetchInnerTubeChat(ctx context.Context) ([]YtChat.ChatMessage, error) {
	type result struct {
		msgs []YtChat.ChatMessage
		cont string
		err  error
	}
	ch := make(chan result, 1)

	go func() {
		msgs, newCont, err := YtChat.FetchContinuationChat(c.innerContinuation, c.innerCfg)
		ch <- result{msgs, newCont, err}
	}()

	select {
	case <-ctx.Done():
		// Sub-goroutine will complete its Sleep naturally; the buffered channel
		// prevents a permanent goroutine leak (it writes and exits).
		return nil, ctx.Err()
	case r := <-ch:
		if r.err == nil {
			c.innerContinuation = r.cont
		}
		return r.msgs, r.err
	}
}

// innerTubePollLoop polls InnerTube for regular chat messages.
// Uses fetchInnerTubeChat (context-aware wrapper).
// Automatically falls back to Data API after innerTubeMaxConsecutiveErrors consecutive errors.
func (c *YouTubeChannel) innerTubePollLoop(ctx context.Context) {
	consecutiveErrors := 0

	for {
		msgs, err := c.fetchInnerTubeChat(ctx)

		if ctx.Err() != nil {
			return
		}

		if errors.Is(err, YtChat.ErrLiveStreamOver) {
			logger.InfoC("youtube", "InnerTube: live stream over")
			c.onStreamEnded(ctx)
			return
		}

		if err != nil {
			consecutiveErrors++
			logger.WarnCF("youtube", "InnerTube poll error", map[string]any{
				"error":              err.Error(),
				"consecutive_errors": consecutiveErrors,
			})

			if consecutiveErrors >= innerTubeMaxConsecutiveErrors {
				logger.ErrorCF("youtube", "InnerTube failed repeatedly, falling back to Data API", map[string]any{
					"errors": consecutiveErrors,
				})
				if c.cancel != nil {
					c.cancel()
				}
				c.fallbackToDataAPI(c.parentCtx)
				return
			}

			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}

		consecutiveErrors = 0

		if len(msgs) == 0 {
			continue
		}

		// ── Convert → existing pipeline ──
		converted := convertInnerTubeMessages(msgs)
		filtered := c.preFilter(converted)

		if len(filtered) == 0 {
			continue
		}

		if c.config.AccumulateComments {
			c.appendToBuffer(filtered)
			continue
		}

		selected := c.selectComments(filtered)
		if c.config.BatchComments && len(selected) > 1 {
			c.batchAndHandle(selected)
		} else {
			for _, item := range selected {
				c.processMessage(item)
			}
		}
	}
}

// fallbackToDataAPI switches from InnerTube to Data API polling.
// Called when InnerTube fails repeatedly (R1) or init fails (R2).
// Uses parentCtx (not pollCtx) to create a fresh polling context.
func (c *YouTubeChannel) fallbackToDataAPI(ctx context.Context) {
	if c.config.APIKey == "" {
		logger.ErrorC("youtube", "Cannot fallback to Data API: no api_key configured")
		c.onStreamEnded(ctx)
		return
	}

	if c.liveChatID == "" {
		liveChatID, err := c.fetchActiveLiveChatID()
		if err != nil {
			logger.ErrorCF("youtube", "Data API fallback failed: cannot get liveChatID", map[string]any{
				"error": err.Error(),
			})
			c.onStreamEnded(ctx)
			return
		}
		c.liveChatID = liveChatID
	}

	logger.InfoCF("youtube", "Switched to Data API polling (fallback mode)", map[string]any{
		"live_chat_id": c.liveChatID,
	})

	pollCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.setRunning(true)
	c.nextPageToken = ""

	go c.pollLoop(pollCtx)
	if c.config.AccumulateComments {
		go c.flushLoop(pollCtx)
	}
}

// superChatPollLoop polls Data API at low frequency for SuperChat events only.
// Uses independent superChatPageToken to avoid conflicts with other polling.
// Auto-stops on quota exceeded (R5B) — InnerTube continues unaffected.
// Stops on ctx.Done() via shared pollCtx (R6).
func (c *YouTubeChannel) superChatPollLoop(ctx context.Context) {
	interval := time.Duration(c.config.SuperchatPollSeconds) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logger.InfoCF("youtube", "SuperChat poll started", map[string]any{
		"interval":     interval.String(),
		"live_chat_id": c.liveChatID,
	})

	for {
		select {
		case <-ctx.Done():
			logger.InfoC("youtube", "SuperChat poll stopped (context cancelled)")
			return
		case <-ticker.C:
		}

		resp, nextToken, err := c.fetchSuperChatMessages()
		if err != nil {
			logger.WarnCF("youtube", "SuperChat poll error", map[string]any{
				"error": err.Error(),
			})
			continue
		}

		if resp.Error != nil {
			if resp.Error.Code == 403 && (strings.Contains(resp.Error.Message, "quota") ||
				strings.Contains(resp.Error.Message, "Exceeded")) {
				logger.WarnC("youtube", "SuperChat poll stopped: API quota exceeded. Regular chat continues via InnerTube.")
				return
			}
			if c.handleAPIError(resp.Error) {
				return
			}
			continue
		}

		c.superChatPageToken = nextToken

		for _, msg := range resp.Items {
			if msg.Snippet.Type == "superChatEvent" {
				c.processMessage(msg)
			}
		}
	}
}

// fetchSuperChatMessages fetches live chat messages using a separate pageToken
// to avoid conflicts with the Data API fallback polling (which uses c.nextPageToken).
func (c *YouTubeChannel) fetchSuperChatMessages() (*youtubeLiveChatResponse, string, error) {
	url := fmt.Sprintf("%s/liveChat/messages?liveChatId=%s&part=snippet,authorDetails&key=%s",
		youtubeAPIBase, c.liveChatID, c.config.APIKey)

	if c.superChatPageToken != "" {
		url += "&pageToken=" + c.superChatPageToken
	}

	resp, err := c.httpClient.Get(url)
	if err != nil {
		return nil, "", fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read response body: %w", err)
	}

	var chatResp youtubeLiveChatResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return nil, "", fmt.Errorf("failed to parse response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && chatResp.Error == nil {
		chatResp.Error = &youtubeAPIError{
			Code:    resp.StatusCode,
			Message: string(body),
		}
	}

	return &chatResp, chatResp.NextPageToken, nil
}

// convertInnerTubeMessages converts InnerTube ChatMessage structs to the existing
// youtubeLiveChatMessage format, allowing reuse of preFilter/selectComments/processMessage.
func convertInnerTubeMessages(msgs []YtChat.ChatMessage) []youtubeLiveChatMessage {
	result := make([]youtubeLiveChatMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Message == "" {
			continue
		}
		var msg youtubeLiveChatMessage
		msg.Snippet.Type = "textMessageEvent"
		msg.Snippet.DisplayMessage = m.Message
		msg.Snippet.TextMessageDetails = &struct {
			MessageText string `json:"messageText"`
		}{MessageText: m.Message}
		msg.Snippet.PublishedAt = m.Timestamp.Format(time.RFC3339)
		msg.AuthorDetails.DisplayName = m.AuthorName
		result = append(result, msg)
	}
	return result
}
