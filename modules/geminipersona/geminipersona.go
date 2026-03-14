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
	"github.com/thomaschourret/discordgobot/db"
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
	db     *db.DBWrapper

	useSystemPrompt bool

	// Simple in-memory thread history cache map[channelID/userID]ChatSession
	sessions   map[string]*ChatSession
	sessionsMu sync.Mutex
}

// Ensure Component implements core.Module
var _ core.Module = (*Component)(nil)

func NewModule(apiKey string, model string, database *db.DBWrapper, useSystemPrompt bool) *Component {
	return &Component{
		apiKey:          apiKey,
		model:           model,
		db:              database,
		useSystemPrompt: useSystemPrompt,
		sessions:        make(map[string]*ChatSession),
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

	// Initialize SQLite table for pre-prompts
	query := `
	CREATE TABLE IF NOT EXISTS geminipersona_prompts (
		guild_id TEXT NOT NULL PRIMARY KEY,
		prompt TEXT NOT NULL
	);
	`
	_, err = m.db.DB.Exec(query)
	if err != nil {
		return fmt.Errorf("failed to create geminipersona_prompts table: %w", err)
	}

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
	defaultMemFuncs := int64(discordgo.PermissionManageChannels)

	return []*discordgo.ApplicationCommand{
		{
			Name:                     "personaprompt",
			Description:              "Set or clear the persona pre-prompt for this server.",
			DefaultMemberPermissions: &defaultMemFuncs,
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "set",
					Description: "Set a system pre-prompt for this server.",
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "prompt",
							Description: "The system behavior instruction (e.g. 'Act like a pirate').",
							Required:    true,
						},
					},
				},
				{
					Name:        "clear",
					Description: "Clear the system pre-prompt for this server.",
					Type:        discordgo.ApplicationCommandOptionSubCommand,
				},
			},
		},
	}
}

func (m *Component) Handlers() map[string]interface{} {
	return map[string]interface{}{
		"MessageCreate": m.handleMessageCreate,
		"personaprompt": m.handlePersonaPromptCommand,
	}
}

func (m *Component) handlePersonaPromptCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	options := i.ApplicationCommandData().Options[0]

	switch options.Name {
	case "set":
		prompt := options.Options[0].StringValue()
		_, err := m.db.DB.Exec(
			"INSERT INTO geminipersona_prompts (guild_id, prompt) VALUES (?, ?) ON CONFLICT(guild_id) DO UPDATE SET prompt=excluded.prompt",
			i.GuildID, prompt,
		)

		var response string
		if err != nil {
			log.Printf("Failed to set persona prompt: %v", err)
			response = "Failed to save the persona pre-prompt."
		} else {
			// Clear sessions for this guild to apply the new prompt
			m.clearSessionsForGuild(i.GuildID)
			response = fmt.Sprintf("Successfully set the persona pre-prompt for this server to:\n> %s", prompt)
		}

		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: response,
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})

	case "clear":
		_, err := m.db.DB.Exec("DELETE FROM geminipersona_prompts WHERE guild_id = ?", i.GuildID)

		var response string
		if err != nil {
			log.Printf("Failed to clear persona prompt: %v", err)
			response = "Failed to clear the persona pre-prompt."
		} else {
			m.clearSessionsForGuild(i.GuildID)
			response = "Successfully cleared the persona pre-prompt for this server."
		}

		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: response,
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
	}
}

func (m *Component) clearSessionsForGuild(guildID string) {
	m.sessionsMu.Lock()
	defer m.sessionsMu.Unlock()

	for key := range m.sessions {
		if strings.HasPrefix(key, guildID+":") {
			delete(m.sessions, key)
		}
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

	s.ChannelTyping(mc.ChannelID)

	key := m.getSessionKey(mc.GuildID, mc.ChannelID, mc.Author.ID)
	response, err := m.generateResponse(key, prompt, mc.GuildID)

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

func (m *Component) getPrePrompt(guildID string) string {
	var prompt string
	err := m.db.DB.QueryRow("SELECT prompt FROM geminipersona_prompts WHERE guild_id = ?", guildID).Scan(&prompt)
	if err == nil {
		return prompt
	}
	return ""
}

func (m *Component) generateResponse(key string, prompt string, guildID string) (string, error) {
	m.sessionsMu.Lock()
	session, exists := m.sessions[key]

	systemPrompt := m.getPrePrompt(guildID)

	if !exists {
		var config *genai.GenerateContentConfig
		if systemPrompt != "" && m.useSystemPrompt {
			config = &genai.GenerateContentConfig{
				SystemInstruction: &genai.Content{
					Parts: []*genai.Part{{Text: systemPrompt}},
				},
			}
		}

		chat, _ := m.client.Chats.Create(context.Background(), m.model, config, nil)
		session = &ChatSession{
			Chat:     chat,
			LastUsed: time.Now(),
		}
		m.sessions[key] = session
	} else {
		session.LastUsed = time.Now()
	}
	m.sessionsMu.Unlock()

	if systemPrompt != "" && !m.useSystemPrompt {
		prompt = fmt.Sprintf("%s\n\n%s", systemPrompt, prompt)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := session.Chat.SendMessage(ctx, genai.Part{Text: prompt})
	if err != nil {
		return "", err
	}

	return resp.Text(), nil
}
