package geminipersona

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/thomaschourret/discordgobot/core"
	"google.golang.org/genai"
)

// ChatSession wraps a genai Chat to track its last activity time for garbage collection
type ChatSession struct {
	Chat     *genai.Chat
	LastUsed time.Time
}

type Component struct {
	apiKey string
	model  string
	client *genai.Client

	// Simple in-memory thread history cache map[channelID/userID]ChatSession
	sessions   map[string]*ChatSession
	sessionsMu sync.Mutex
}

// Ensure Component implements core.Module
var _ core.Module = (*Component)(nil)

func NewModule(apiKey string, model string) *Component {
	return &Component{
		apiKey:   apiKey,
		model:    model,
		sessions: make(map[string]*ChatSession),
	}
}

func (m *Component) Name() string {
	return "Gemini Persona"
}

func (m *Component) Description() string {
	return "Conversational AI that acts like a person on mentions, without system prompts."
}

func (m *Component) Init(session *discordgo.Session) error {
	if m.apiKey == "" {
		return fmt.Errorf("GEMINI_API_KEY is not set. Module disabled")
	}

	ctx := context.Background()
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey: m.apiKey,
	})
	if err != nil {
		return fmt.Errorf("failed to create genai client: %w", err)
	}

	m.client = client

	// Start background cleanup goroutine
	go m.cleanupStaleSessions()

	return nil
}

// cleanupStaleSessions periodically removes sessions inactive for over 24 hours to prevent memory leaks
func (m *Component) cleanupStaleSessions() {
	ticker := time.NewTicker(1 * time.Hour)
	for range ticker.C {
		m.sessionsMu.Lock()
		for key, session := range m.sessions {
			if time.Since(session.LastUsed) > 24*time.Hour {
				delete(m.sessions, key)
			}
		}
		m.sessionsMu.Unlock()
	}
}

func (m *Component) Enable()  {}
func (m *Component) Disable() {}
func (m *Component) IsEnabled() bool {
	return m.apiKey != ""
}

func (m *Component) RegisterCommands() []*discordgo.ApplicationCommand {
	return []*discordgo.ApplicationCommand{}
}

func (m *Component) Handlers() map[string]interface{} {
	return map[string]interface{}{
		"MessageCreate": m.handleMessageCreate,
	}
}

func (m *Component) handleMessageCreate(s *discordgo.Session, mc *discordgo.MessageCreate) {
	if mc.Author.Bot {
		return
	}

	// Check if the bot is explicitly mentioned
	isMentioned := false
	for _, user := range mc.Mentions {
		if user.ID == s.State.User.ID {
			isMentioned = true
			break
		}
	}

	if !isMentioned {
		return
	}

	// Remove the bot's mention from the text
	prompt := strings.ReplaceAll(mc.Content, fmt.Sprintf("<@%s>", s.State.User.ID), "")
	prompt = strings.TrimSpace(prompt)

	if prompt == "" {
		s.ChannelMessageSend(mc.ChannelID, "Hey there! Need something?")
		return
	}

	// Send typing indicator
	s.ChannelTyping(mc.ChannelID)

	key := m.getSessionKey(mc.GuildID, mc.ChannelID, mc.Author.ID)
	response, err := m.generateResponse(key, prompt)

	if err != nil {
		log.Printf("Gemini Persona generation error: %v", err)
		s.ChannelMessageSendReply(mc.ChannelID, "Sorry, I can't really talk right now.", mc.Reference())
		return
	}

	runes := []rune(response)
	var chunks []string
	for i := 0; i < len(runes); i += 2000 {
		end := i + 2000
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[i:end]))
	}

	for i, chunk := range chunks {
		if i == 0 {
			s.ChannelMessageSendReply(mc.ChannelID, chunk, mc.Reference())
		} else {
			s.ChannelMessageSend(mc.ChannelID, chunk)
		}
	}
}

func (m *Component) getSessionKey(guildID, channelID, userID string) string {
	return fmt.Sprintf("%s:%s:%s", guildID, channelID, userID)
}

func (m *Component) generateResponse(key string, prompt string) (string, error) {
	m.sessionsMu.Lock()
	session, exists := m.sessions[key]

	if !exists {
		// No system instructions configured, just pass nil for config to avoid restrictions
		chat, _ := m.client.Chats.Create(context.Background(), m.model, nil, nil)
		session = &ChatSession{
			Chat:     chat,
			LastUsed: time.Now(),
		}
		m.sessions[key] = session
	} else {
		session.LastUsed = time.Now()
	}
	m.sessionsMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := session.Chat.SendMessage(ctx, genai.Part{Text: prompt})
	if err != nil {
		return "", err
	}

	return resp.Text(), nil
}
