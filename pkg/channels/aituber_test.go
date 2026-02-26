package channels

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestParseEmotion(t *testing.T) {
	tests := []struct {
		name           string
		content        string
		defaultEmotion string
		wantText       string
		wantEmotion    string
	}{
		{
			name:           "happy tag",
			content:        "[happy] すごいですね！",
			defaultEmotion: "neutral",
			wantText:       "すごいですね！",
			wantEmotion:    "happy",
		},
		{
			name:           "sad tag",
			content:        "[sad] 残念です",
			defaultEmotion: "neutral",
			wantText:       "残念です",
			wantEmotion:    "sad",
		},
		{
			name:           "no tag uses default",
			content:        "普通の応答です",
			defaultEmotion: "neutral",
			wantText:       "普通の応答です",
			wantEmotion:    "neutral",
		},
		{
			name:           "invalid tag uses default",
			content:        "[unknown] テスト",
			defaultEmotion: "neutral",
			wantText:       "[unknown] テスト",
			wantEmotion:    "neutral",
		},
		{
			name:           "empty default becomes neutral",
			content:        "テスト",
			defaultEmotion: "",
			wantText:       "テスト",
			wantEmotion:    "neutral",
		},
		{
			name:           "uppercase tag normalized",
			content:        "[HAPPY] 大文字タグ",
			defaultEmotion: "neutral",
			wantText:       "大文字タグ",
			wantEmotion:    "happy",
		},
		{
			name:           "surprised tag",
			content:        "[surprised] えっ！？",
			defaultEmotion: "neutral",
			wantText:       "えっ！？",
			wantEmotion:    "surprised",
		},
		{
			name:           "empty content",
			content:        "",
			defaultEmotion: "neutral",
			wantText:       "",
			wantEmotion:    "neutral",
		},
		{
			name:           "bracket without closing",
			content:        "[happy テスト",
			defaultEmotion: "neutral",
			wantText:       "[happy テスト",
			wantEmotion:    "neutral",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotText, gotEmotion := parseEmotion(tt.content, tt.defaultEmotion)
			if gotText != tt.wantText {
				t.Errorf("parseEmotion() text = %q, want %q", gotText, tt.wantText)
			}
			if gotEmotion != tt.wantEmotion {
				t.Errorf("parseEmotion() emotion = %q, want %q", gotEmotion, tt.wantEmotion)
			}
		})
	}
}

func TestNewAITuberChannel(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cfg := config.AITuberConfig{
		Enabled:        true,
		WSHost:         "127.0.0.1",
		WSPort:         0,
		WSPath:         "/ws",
		DefaultEmotion: "neutral",
		MaxQueueSize:   5,
	}

	ch, err := NewAITuberChannel(cfg, msgBus)
	if err != nil {
		t.Fatalf("NewAITuberChannel() error = %v", err)
	}
	if ch.Name() != "aituber" {
		t.Errorf("Name() = %q, want %q", ch.Name(), "aituber")
	}
}

func TestAITuberSendQueueDrop(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cfg := config.AITuberConfig{
		Enabled:        true,
		WSHost:         "127.0.0.1",
		WSPort:         0,
		WSPath:         "/ws",
		DefaultEmotion: "neutral",
		MaxQueueSize:   2,
	}

	ch, err := NewAITuberChannel(cfg, msgBus)
	if err != nil {
		t.Fatalf("NewAITuberChannel() error = %v", err)
	}

	// Fill queue
	ch.Send(context.Background(), bus.OutboundMessage{Content: "msg1"})
	ch.Send(context.Background(), bus.OutboundMessage{Content: "msg2"})
	// This should drop oldest and add new
	ch.Send(context.Background(), bus.OutboundMessage{Content: "msg3"})

	// Drain queue and verify msg3 is present
	found := false
	for i := 0; i < 2; i++ {
		select {
		case msg := <-ch.sendQueue:
			if msg.Text == "msg3" {
				found = true
			}
		default:
		}
	}
	if !found {
		t.Error("Expected msg3 to be in queue after drop")
	}
}

func TestAITuberWebSocketIntegration(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cfg := config.AITuberConfig{
		Enabled:        true,
		WSHost:         "127.0.0.1",
		WSPort:         18999,
		WSPath:         "/ws",
		DefaultEmotion: "neutral",
		MaxQueueSize:   10,
	}

	ch, err := NewAITuberChannel(cfg, msgBus)
	if err != nil {
		t.Fatalf("NewAITuberChannel() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ch.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer ch.Stop(context.Background())

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

	// Connect WebSocket client
	dialer := websocket.DefaultDialer
	conn, resp, err := dialer.Dial("ws://127.0.0.1:18999/ws", nil)
	if resp != nil {
		resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("WebSocket dial error = %v", err)
	}
	defer conn.Close()

	// Wait for connection to register
	time.Sleep(50 * time.Millisecond)

	// Send a message with emotion tag
	ch.Send(context.Background(), bus.OutboundMessage{
		Content: "[happy] テストメッセージ",
	})

	// Read the message from WebSocket
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage() error = %v", err)
	}

	var msg aituberMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("Unmarshal error = %v", err)
	}

	if msg.Text != "テストメッセージ" {
		t.Errorf("Text = %q, want %q", msg.Text, "テストメッセージ")
	}
	if msg.Emotion != "happy" {
		t.Errorf("Emotion = %q, want %q", msg.Emotion, "happy")
	}
	if msg.Role != "assistant" {
		t.Errorf("Role = %q, want %q", msg.Role, "assistant")
	}
	if msg.Type != "message" {
		t.Errorf("Type = %q, want %q", msg.Type, "message")
	}

	// Send TTS complete callback
	ttsComplete := map[string]string{"type": "tts_complete"}
	ttsData, _ := json.Marshal(ttsComplete)
	if err := conn.WriteMessage(websocket.TextMessage, ttsData); err != nil {
		t.Fatalf("WriteMessage() error = %v", err)
	}

	// Verify sendWorker can proceed (send another message)
	ch.Send(context.Background(), bus.OutboundMessage{
		Content: "[neutral] 2番目のメッセージ",
	})

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data2, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage() second error = %v", err)
	}

	var msg2 aituberMessage
	if err := json.Unmarshal(data2, &msg2); err != nil {
		t.Fatalf("Unmarshal second error = %v", err)
	}

	if msg2.Text != "2番目のメッセージ" {
		t.Errorf("Second Text = %q, want %q", msg2.Text, "2番目のメッセージ")
	}
}

func TestAITuberHealthEndpoint(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cfg := config.AITuberConfig{
		Enabled:        true,
		WSHost:         "127.0.0.1",
		WSPort:         18998,
		WSPath:         "/ws",
		DefaultEmotion: "neutral",
		MaxQueueSize:   10,
	}

	ch, err := NewAITuberChannel(cfg, msgBus)
	if err != nil {
		t.Fatalf("NewAITuberChannel() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ch.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer ch.Stop(context.Background())

	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get("http://127.0.0.1:18998/health/aituber")
	if err != nil {
		t.Fatalf("Health check error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Health status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var health map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("Decode error = %v", err)
	}

	if health["status"] != "ok" {
		t.Errorf("Health status = %v, want ok", health["status"])
	}
}

func TestAITuberBroadcastNoClients(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cfg := config.AITuberConfig{
		Enabled:        true,
		WSHost:         "127.0.0.1",
		WSPort:         18997,
		WSPath:         "/ws",
		DefaultEmotion: "neutral",
		MaxQueueSize:   10,
	}

	ch, err := NewAITuberChannel(cfg, msgBus)
	if err != nil {
		t.Fatalf("NewAITuberChannel() error = %v", err)
	}

	// broadcastJSON with no clients should return 0
	sent := ch.broadcastJSON(aituberMessage{Text: "test", Emotion: "neutral"})
	if sent != 0 {
		t.Errorf("broadcastJSON with no clients = %d, want 0", sent)
	}
}

func TestAITuberMultipleClients(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cfg := config.AITuberConfig{
		Enabled:        true,
		WSHost:         "127.0.0.1",
		WSPort:         18996,
		WSPath:         "/ws",
		DefaultEmotion: "neutral",
		MaxQueueSize:   10,
	}

	ch, err := NewAITuberChannel(cfg, msgBus)
	if err != nil {
		t.Fatalf("NewAITuberChannel() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ch.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer ch.Stop(context.Background())

	time.Sleep(100 * time.Millisecond)

	// Connect two clients
	dialer := websocket.DefaultDialer
	var conns []*websocket.Conn
	for i := 0; i < 2; i++ {
		conn, resp, err := dialer.Dial("ws://127.0.0.1:18996/ws", nil)
		if resp != nil {
			resp.Body.Close()
		}
		if err != nil {
			t.Fatalf("Dial client %d error = %v", i, err)
		}
		conns = append(conns, conn)
	}
	defer func() {
		for _, conn := range conns {
			conn.Close()
		}
	}()

	time.Sleep(50 * time.Millisecond)

	// Broadcast a message
	sent := ch.broadcastJSON(aituberMessage{Text: "broadcast test", Emotion: "happy", Role: "assistant", Type: "message"})
	if sent != 2 {
		t.Errorf("broadcastJSON to 2 clients = %d, want 2", sent)
	}

	// Both clients should receive the message
	var wg sync.WaitGroup
	received := make([]string, 2)
	for i, conn := range conns {
		wg.Add(1)
		go func(idx int, c *websocket.Conn) {
			defer wg.Done()
			c.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, data, err := c.ReadMessage()
			if err != nil {
				t.Errorf("Client %d ReadMessage error = %v", idx, err)
				return
			}
			var msg aituberMessage
			json.Unmarshal(data, &msg)
			received[idx] = msg.Text
		}(i, conn)
	}
	wg.Wait()

	for i, text := range received {
		if text != "broadcast test" {
			t.Errorf("Client %d received = %q, want %q", i, text, "broadcast test")
		}
	}
}

func TestAITuberEmotionTagWithExtraSpaces(t *testing.T) {
	text, emotion := parseEmotion("[happy]   スペース多め", "neutral")
	if text != "スペース多め" {
		t.Errorf("text = %q, want %q", text, "スペース多め")
	}
	if emotion != "happy" {
		t.Errorf("emotion = %q, want %q", emotion, "happy")
	}
}

func TestAITuberSendWithDefaultEmotion(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cfg := config.AITuberConfig{
		Enabled:        true,
		WSHost:         "127.0.0.1",
		WSPort:         0,
		WSPath:         "/ws",
		DefaultEmotion: "relaxed",
		MaxQueueSize:   5,
	}

	ch, err := NewAITuberChannel(cfg, msgBus)
	if err != nil {
		t.Fatalf("NewAITuberChannel() error = %v", err)
	}

	ch.Send(context.Background(), bus.OutboundMessage{Content: "タグなしメッセージ"})

	msg := <-ch.sendQueue
	if msg.Emotion != "relaxed" {
		t.Errorf("Emotion = %q, want %q", msg.Emotion, "relaxed")
	}
	if msg.Text != "タグなしメッセージ" {
		t.Errorf("Text = %q, want %q", msg.Text, "タグなしメッセージ")
	}
}

// TestAITuberCORSOrigin verifies that WebSocket connections from any origin are accepted.
func TestAITuberCORSOrigin(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cfg := config.AITuberConfig{
		Enabled:        true,
		WSHost:         "127.0.0.1",
		WSPort:         18995,
		WSPath:         "/ws",
		DefaultEmotion: "neutral",
		MaxQueueSize:   10,
	}

	ch, err := NewAITuberChannel(cfg, msgBus)
	if err != nil {
		t.Fatalf("NewAITuberChannel() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ch.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer ch.Stop(context.Background())

	time.Sleep(100 * time.Millisecond)

	// Connect with a different origin header (simulates cross-origin browser connection)
	header := http.Header{}
	header.Set("Origin", "http://192.168.1.100:3000")

	dialer := websocket.DefaultDialer
	conn, resp, err := dialer.Dial("ws://127.0.0.1:18995/ws", header)
	if resp != nil {
		resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("Cross-origin dial should succeed, got error = %v", err)
	}
	conn.Close()
}

// TestAITuberClientDisconnectCleanup verifies that disconnected clients are cleaned up.
func TestAITuberClientDisconnectCleanup(t *testing.T) {
	msgBus := bus.NewMessageBus()
	cfg := config.AITuberConfig{
		Enabled:        true,
		WSHost:         "127.0.0.1",
		WSPort:         18994,
		WSPath:         "/ws",
		DefaultEmotion: "neutral",
		MaxQueueSize:   10,
	}

	ch, err := NewAITuberChannel(cfg, msgBus)
	if err != nil {
		t.Fatalf("NewAITuberChannel() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ch.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer ch.Stop(context.Background())

	time.Sleep(100 * time.Millisecond)

	// Connect and then disconnect
	dialer := websocket.DefaultDialer
	conn, resp, err := dialer.Dial("ws://127.0.0.1:18994/ws", nil)
	if resp != nil {
		resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("Dial error = %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	ch.clientsMu.RLock()
	beforeCount := len(ch.clients)
	ch.clientsMu.RUnlock()

	if beforeCount != 1 {
		t.Fatalf("Expected 1 client before disconnect, got %d", beforeCount)
	}

	// Close client connection
	conn.Close()

	// Wait for readPump to detect disconnect and clean up
	time.Sleep(200 * time.Millisecond)

	ch.clientsMu.RLock()
	afterCount := len(ch.clients)
	ch.clientsMu.RUnlock()

	if afterCount != 0 {
		t.Errorf("Expected 0 clients after disconnect, got %d", afterCount)
	}
}

// TestParseEmotionAllValidTags ensures all valid emotion tags are recognized.
func TestParseEmotionAllValidTags(t *testing.T) {
	validTags := []string{"neutral", "happy", "sad", "angry", "relaxed", "surprised"}
	for _, tag := range validTags {
		content := "[" + tag + "] test"
		_, emotion := parseEmotion(content, "neutral")
		if emotion != tag {
			t.Errorf("parseEmotion(%q) emotion = %q, want %q", content, emotion, tag)
		}
	}
}

// TestParseEmotionSquareBracketsInText handles content that starts with [ but isn't a tag.
func TestParseEmotionSquareBracketsInText(t *testing.T) {
	tests := []struct {
		content     string
		wantText    string
		wantEmotion string
	}{
		{"[配列] テスト", "[配列] テスト", "neutral"},
		{"[]空タグ", "[]空タグ", "neutral"},
		{"[a]短すぎ", "[a]短すぎ", "neutral"},
	}

	for _, tt := range tests {
		text, emotion := parseEmotion(tt.content, "neutral")
		if text != tt.wantText || emotion != tt.wantEmotion {
			t.Errorf("parseEmotion(%q) = (%q, %q), want (%q, %q)",
				tt.content, text, emotion, tt.wantText, tt.wantEmotion)
		}
	}
}

// TestAITuberStopClosesAllClients ensures Stop closes all WebSocket connections.
func TestAITuberStopClosesAllClients(t *testing.T) {
	if strings.Contains(t.Name(), "race") {
		t.Skip("Skipping in race mode")
	}

	msgBus := bus.NewMessageBus()
	cfg := config.AITuberConfig{
		Enabled:        true,
		WSHost:         "127.0.0.1",
		WSPort:         18993,
		WSPath:         "/ws",
		DefaultEmotion: "neutral",
		MaxQueueSize:   10,
	}

	ch, err := NewAITuberChannel(cfg, msgBus)
	if err != nil {
		t.Fatalf("NewAITuberChannel() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ch.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Connect a client
	dialer := websocket.DefaultDialer
	conn, resp, err := dialer.Dial("ws://127.0.0.1:18993/ws", nil)
	if resp != nil {
		resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("Dial error = %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	// Stop channel - should close all clients
	ch.Stop(context.Background())

	// Try to read - should get an error
	conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Error("Expected read error after Stop(), got nil")
	}
	conn.Close()
}
