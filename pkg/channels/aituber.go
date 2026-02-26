package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// AITuberChannel implements the Channel interface for AITuber Kit integration.
// It runs a WebSocket server that AITuber Kit connects to in external linkage mode.
// Messages are sent with emotion tags and TTS completion callbacks control flow.
type AITuberChannel struct {
	*BaseChannel
	config    config.AITuberConfig
	upgrader  websocket.Upgrader
	clients   map[*websocket.Conn]bool
	clientsMu sync.RWMutex
	server    *http.Server
	ctx       context.Context
	cancel    context.CancelFunc
	sendQueue chan aituberMessage
	ttsDone   chan struct{}
}

type aituberMessage struct {
	Text    string `json:"text"`
	Role    string `json:"role"`
	Emotion string `json:"emotion"`
	Type    string `json:"type"`
}

type aituberEvent struct {
	Type string `json:"type"`
}

var validEmotions = map[string]bool{
	"neutral":   true,
	"happy":     true,
	"sad":       true,
	"angry":     true,
	"relaxed":   true,
	"surprised": true,
}

func NewAITuberChannel(cfg config.AITuberConfig, msgBus *bus.MessageBus) (*AITuberChannel, error) {
	base := NewBaseChannel("aituber", cfg, msgBus, nil)
	queueSize := cfg.MaxQueueSize
	if queueSize <= 0 {
		queueSize = 10
	}

	return &AITuberChannel{
		BaseChannel: base,
		config:      cfg,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		clients:   make(map[*websocket.Conn]bool),
		sendQueue: make(chan aituberMessage, queueSize),
		ttsDone:   make(chan struct{}, 1),
	}, nil
}

func (c *AITuberChannel) Start(ctx context.Context) error {
	logger.InfoC("aituber", "Starting AITuber channel...")

	c.ctx, c.cancel = context.WithCancel(ctx)

	mux := http.NewServeMux()
	wsPath := c.config.WSPath
	if wsPath == "" {
		wsPath = "/ws"
	}
	mux.HandleFunc(wsPath, c.handleWS)
	mux.HandleFunc("/health/aituber", c.handleHealth)

	addr := fmt.Sprintf("%s:%d", c.config.WSHost, c.config.WSPort)
	c.server = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	c.setRunning(true)
	logger.InfoCF("aituber", "AITuber channel started", map[string]any{
		"address": addr,
		"path":    wsPath,
	})

	go func() {
		if err := c.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.ErrorCF("aituber", "HTTP server error", map[string]any{
				"error": err.Error(),
			})
		}
	}()

	go c.sendWorker(c.ctx)

	return nil
}

func (c *AITuberChannel) Stop(ctx context.Context) error {
	logger.InfoC("aituber", "Stopping AITuber channel...")

	if c.cancel != nil {
		c.cancel()
	}

	if c.server != nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		c.server.Shutdown(shutdownCtx)
	}

	c.clientsMu.Lock()
	for conn := range c.clients {
		conn.Close()
		delete(c.clients, conn)
	}
	c.clientsMu.Unlock()

	c.setRunning(false)
	logger.InfoC("aituber", "AITuber channel stopped")
	return nil
}

func (c *AITuberChannel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	text, emotion := parseEmotion(msg.Content, c.config.DefaultEmotion)
	m := aituberMessage{
		Text:    text,
		Role:    "assistant",
		Emotion: emotion,
		Type:    "message",
	}
	select {
	case c.sendQueue <- m:
	default:
		<-c.sendQueue
		c.sendQueue <- m
		logger.WarnC("aituber", "Send queue full, dropped oldest message")
	}
	return nil
}

func (c *AITuberChannel) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := c.upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.ErrorCF("aituber", "WebSocket upgrade failed", map[string]any{
			"error": err.Error(),
		})
		return
	}

	c.clientsMu.Lock()
	c.clients[conn] = true
	clientCount := len(c.clients)
	c.clientsMu.Unlock()

	logger.InfoCF("aituber", "Client connected", map[string]any{
		"remote_addr":  r.RemoteAddr,
		"total_clients": clientCount,
	})

	go c.readPump(conn)
}

func (c *AITuberChannel) handleHealth(w http.ResponseWriter, r *http.Request) {
	c.clientsMu.RLock()
	clientCount := len(c.clients)
	c.clientsMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"clients": clientCount,
	})
}

// readPump reads messages from a single WebSocket client.
// It handles TTS completion callbacks from AITuber Kit.
func (c *AITuberChannel) readPump(conn *websocket.Conn) {
	defer func() {
		c.clientsMu.Lock()
		delete(c.clients, conn)
		clientCount := len(c.clients)
		c.clientsMu.Unlock()
		conn.Close()

		logger.InfoCF("aituber", "Client disconnected", map[string]any{
			"total_clients": clientCount,
		})
	}()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				logger.DebugCF("aituber", "Read error", map[string]any{
					"error": err.Error(),
				})
			}
			break
		}
		var event aituberEvent
		if json.Unmarshal(message, &event) == nil && event.Type == "tts_complete" {
			select {
			case c.ttsDone <- struct{}{}:
			default:
			}
		}
	}
}

// sendWorker processes the send queue and waits for TTS completion between messages.
func (c *AITuberChannel) sendWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-c.sendQueue:
			sent := c.broadcastJSON(msg)
			if sent == 0 {
				continue
			}
			select {
			case <-c.ttsDone:
			case <-time.After(30 * time.Second):
				logger.WarnC("aituber", "TTS completion timeout, proceeding")
			case <-ctx.Done():
				return
			}
		}
	}
}

// broadcastJSON sends a JSON message to all connected clients.
// Returns the number of clients the message was successfully sent to.
func (c *AITuberChannel) broadcastJSON(msg aituberMessage) int {
	data, err := json.Marshal(msg)
	if err != nil {
		logger.ErrorCF("aituber", "Failed to marshal message", map[string]any{
			"error": err.Error(),
		})
		return 0
	}

	c.clientsMu.Lock()
	defer c.clientsMu.Unlock()

	sent := 0
	var failed []*websocket.Conn
	for conn := range c.clients {
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			failed = append(failed, conn)
		} else {
			sent++
		}
	}

	for _, conn := range failed {
		conn.Close()
		delete(c.clients, conn)
	}

	return sent
}

// parseEmotion extracts emotion tag from content.
// Format: "[happy] text" → ("text", "happy")
// If no valid tag found, returns content unchanged with defaultEmotion.
func parseEmotion(content, defaultEmotion string) (string, string) {
	if defaultEmotion == "" {
		defaultEmotion = "neutral"
	}
	if len(content) > 2 && content[0] == '[' {
		if end := strings.Index(content, "]"); end > 0 {
			tag := strings.ToLower(content[1:end])
			if validEmotions[tag] {
				return strings.TrimSpace(content[end+1:]), tag
			}
		}
	}
	return content, defaultEmotion
}
