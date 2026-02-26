package channels

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func newTestYouTubeChannel(cfg config.YouTubeConfig) *YouTubeChannel {
	msgBus := bus.NewMessageBus()
	base := NewBaseChannel("youtube", cfg, msgBus, cfg.AllowFrom)
	return &YouTubeChannel{
		BaseChannel: base,
		config:      cfg,
	}
}

func makeMessage(author, text string) youtubeLiveChatMessage {
	msg := youtubeLiveChatMessage{}
	msg.Snippet.Type = "textMessageEvent"
	msg.Snippet.DisplayMessage = text
	msg.Snippet.TextMessageDetails = &struct {
		MessageText string `json:"messageText"`
	}{MessageText: text}
	msg.AuthorDetails.DisplayName = author
	msg.AuthorDetails.ChannelID = "UC" + author
	return msg
}

func makeSuperChat(author, text, amount string) youtubeLiveChatMessage {
	msg := makeMessage(author, text)
	msg.Snippet.Type = "superChatEvent"
	msg.Snippet.SuperChatDetails = &struct {
		AmountMicros        string `json:"amountMicros"`
		Currency            string `json:"currency"`
		AmountDisplayString string `json:"amountDisplayString"`
		UserComment         string `json:"userComment"`
	}{AmountDisplayString: amount, UserComment: text}
	return msg
}

func makeOwnerMessage(author, text string) youtubeLiveChatMessage {
	msg := makeMessage(author, text)
	msg.AuthorDetails.IsChatOwner = true
	return msg
}

func makeModeratorMessage(author, text string) youtubeLiveChatMessage {
	msg := makeMessage(author, text)
	msg.AuthorDetails.IsChatModerator = true
	return msg
}

func TestPreFilterNGWords(t *testing.T) {
	ch := newTestYouTubeChannel(config.YouTubeConfig{
		NGWords: []string{"spam", "bad"},
	})

	items := []youtubeLiveChatMessage{
		makeMessage("user1", "hello world"),
		makeMessage("user2", "this is SPAM"),
		makeMessage("user3", "bad content here"),
		makeMessage("user4", "good message"),
	}

	filtered := ch.preFilter(items)
	if len(filtered) != 2 {
		t.Errorf("preFilter NGWords: got %d, want 2", len(filtered))
	}
}

func TestPreFilterMinLength(t *testing.T) {
	ch := newTestYouTubeChannel(config.YouTubeConfig{
		MinMessageLength: 5,
	})

	items := []youtubeLiveChatMessage{
		makeMessage("user1", "hi"),
		makeMessage("user2", "hello world"),
		makeMessage("user3", "あ"),
		makeMessage("user4", "こんにちは世界"),
	}

	filtered := ch.preFilter(items)
	if len(filtered) != 2 {
		t.Errorf("preFilter MinLength: got %d, want 2", len(filtered))
	}
}

func TestPreFilterBlockURLs(t *testing.T) {
	ch := newTestYouTubeChannel(config.YouTubeConfig{
		BlockURLs: true,
	})

	items := []youtubeLiveChatMessage{
		makeMessage("user1", "check out http://example.com"),
		makeMessage("user2", "visit https://example.com"),
		makeMessage("user3", "normal message"),
		makeMessage("user4", "no url here"),
	}

	filtered := ch.preFilter(items)
	if len(filtered) != 2 {
		t.Errorf("preFilter BlockURLs: got %d, want 2", len(filtered))
	}
}

func TestPreFilterMaxRepeatRatio(t *testing.T) {
	ch := newTestYouTubeChannel(config.YouTubeConfig{
		MaxRepeatRatio: 0.5,
	})

	items := []youtubeLiveChatMessage{
		makeMessage("user1", "aaaaa"),
		makeMessage("user2", "abcde"),
		makeMessage("user3", "wwwwwwwwww"),
		makeMessage("user4", "こんにちは"),
	}

	filtered := ch.preFilter(items)
	if len(filtered) != 2 {
		t.Errorf("preFilter MaxRepeatRatio: got %d, want 2", len(filtered))
	}
}

func TestPreFilterNoRules(t *testing.T) {
	ch := newTestYouTubeChannel(config.YouTubeConfig{})

	items := []youtubeLiveChatMessage{
		makeMessage("user1", "hello"),
		makeMessage("user2", "world"),
	}

	filtered := ch.preFilter(items)
	if len(filtered) != 2 {
		t.Errorf("preFilter no rules: got %d, want 2", len(filtered))
	}
}

func TestPreFilterCombinedRules(t *testing.T) {
	ch := newTestYouTubeChannel(config.YouTubeConfig{
		NGWords:          []string{"spam"},
		MinMessageLength: 3,
		BlockURLs:        true,
	})

	items := []youtubeLiveChatMessage{
		makeMessage("user1", "hi"),                        // too short
		makeMessage("user2", "this is spam"),              // ng word
		makeMessage("user3", "https://example.com check"), // url
		makeMessage("user4", "good message here"),         // passes all
	}

	filtered := ch.preFilter(items)
	if len(filtered) != 1 {
		t.Errorf("preFilter combined: got %d, want 1", len(filtered))
	}
	if len(filtered) > 0 && filtered[0].AuthorDetails.DisplayName != "user4" {
		t.Errorf("preFilter combined: expected user4, got %s", filtered[0].AuthorDetails.DisplayName)
	}
}

func TestSelectCommentsLatest(t *testing.T) {
	ch := newTestYouTubeChannel(config.YouTubeConfig{
		MaxCommentsPerPoll: 2,
		SelectionStrategy:  "latest",
	})

	msgs := []youtubeLiveChatMessage{
		makeMessage("user1", "first"),
		makeMessage("user2", "second"),
		makeMessage("user3", "third"),
		makeMessage("user4", "fourth"),
	}

	selected := ch.selectComments(msgs)
	if len(selected) != 2 {
		t.Fatalf("selectComments latest: got %d, want 2", len(selected))
	}
	if selected[0].AuthorDetails.DisplayName != "user3" {
		t.Errorf("selectComments latest[0]: got %s, want user3", selected[0].AuthorDetails.DisplayName)
	}
	if selected[1].AuthorDetails.DisplayName != "user4" {
		t.Errorf("selectComments latest[1]: got %s, want user4", selected[1].AuthorDetails.DisplayName)
	}
}

func TestSelectCommentsPriority(t *testing.T) {
	ch := newTestYouTubeChannel(config.YouTubeConfig{
		MaxCommentsPerPoll: 2,
		SelectionStrategy:  "priority",
	})

	msgs := []youtubeLiveChatMessage{
		makeMessage("user1", "normal message"),
		makeSuperChat("user2", "super chat", "¥500"),
		makeMessage("user3", "another normal"),
		makeOwnerMessage("owner", "owner message"),
	}

	selected := ch.selectComments(msgs)
	if len(selected) != 2 {
		t.Fatalf("selectComments priority: got %d, want 2", len(selected))
	}
	// Super chat and owner should be first
	hasSuperChat := false
	hasOwner := false
	for _, s := range selected {
		if s.AuthorDetails.DisplayName == "user2" {
			hasSuperChat = true
		}
		if s.AuthorDetails.DisplayName == "owner" {
			hasOwner = true
		}
	}
	if !hasSuperChat || !hasOwner {
		t.Errorf("selectComments priority: expected super chat and owner, got %+v", selected)
	}
}

func TestSelectCommentsRandom(t *testing.T) {
	ch := newTestYouTubeChannel(config.YouTubeConfig{
		MaxCommentsPerPoll: 2,
		SelectionStrategy:  "random",
	})

	msgs := []youtubeLiveChatMessage{
		makeMessage("user1", "first"),
		makeMessage("user2", "second"),
		makeMessage("user3", "third"),
		makeMessage("user4", "fourth"),
	}

	selected := ch.selectComments(msgs)
	if len(selected) != 2 {
		t.Errorf("selectComments random: got %d, want 2", len(selected))
	}
}

func TestSelectCommentsNoLimit(t *testing.T) {
	ch := newTestYouTubeChannel(config.YouTubeConfig{
		MaxCommentsPerPoll: 0,
	})

	msgs := []youtubeLiveChatMessage{
		makeMessage("user1", "first"),
		makeMessage("user2", "second"),
	}

	selected := ch.selectComments(msgs)
	if len(selected) != 2 {
		t.Errorf("selectComments no limit: got %d, want 2", len(selected))
	}
}

func TestSelectCommentsFewMessages(t *testing.T) {
	ch := newTestYouTubeChannel(config.YouTubeConfig{
		MaxCommentsPerPoll: 5,
		SelectionStrategy:  "latest",
	})

	msgs := []youtubeLiveChatMessage{
		makeMessage("user1", "first"),
		makeMessage("user2", "second"),
	}

	selected := ch.selectComments(msgs)
	if len(selected) != 2 {
		t.Errorf("selectComments few messages: got %d, want 2", len(selected))
	}
}

func TestSelectCommentsPriorityWithModerator(t *testing.T) {
	ch := newTestYouTubeChannel(config.YouTubeConfig{
		MaxCommentsPerPoll: 1,
		SelectionStrategy:  "priority",
	})

	msgs := []youtubeLiveChatMessage{
		makeMessage("user1", "normal message"),
		makeModeratorMessage("mod", "moderator message"),
	}

	selected := ch.selectComments(msgs)
	if len(selected) != 1 {
		t.Fatalf("selectComments priority mod: got %d, want 1", len(selected))
	}
	if selected[0].AuthorDetails.DisplayName != "mod" {
		t.Errorf("selectComments priority mod: got %s, want mod", selected[0].AuthorDetails.DisplayName)
	}
}
