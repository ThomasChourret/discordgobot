package gemini

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
	return "Gemini AI"
}

func (m *Component) Description() string {
	return "Conversational AI integration using Google Gemini."
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
	CREATE TABLE IF NOT EXISTS gemini_prompts (
		guild_id TEXT NOT NULL,
		channel_id TEXT NOT NULL,
		prompt TEXT NOT NULL,
		PRIMARY KEY (guild_id, channel_id)
	);
	`
	_, err = m.db.DB.Exec(query)
	if err != nil {
		return fmt.Errorf("failed to create gemini_prompts table: %w", err)
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
			Name:        "chat",
			Description: "Chat with Gemini AI",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "prompt",
					Description: "Your message to the AI",
					Required:    true,
				},
			},
		},
		{
			Name:        "chat_reset",
			Description: "Reset your conversational history with Gemini",
		},
		{
			Name:                     "setprompt",
			Description:              "Set a system pre-prompt for Gemini in this server and/or channel.",
			DefaultMemberPermissions: &defaultMemFuncs,
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "prompt",
					Description: "The system behavior instruction (e.g. 'Act like a pirate').",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionChannel,
					Name:        "channel",
					Description: "The specific channel to apply this prompt to. If omitted, applies to the whole server.",
					Required:    false,
				},
			},
		},
		{
			Name:                     "clearprompt",
			Description:              "Clear the system pre-prompt for Gemini in this server and/or channel.",
			DefaultMemberPermissions: &defaultMemFuncs,
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionChannel,
					Name:        "channel",
					Description: "The specific channel to clear the prompt from. If omitted, clears the server-wide prompt.",
					Required:    false,
				},
			},
		},
		{
			Name:                     "geminimodels",
			Description:              "List available Gemini AI models.",
			DefaultMemberPermissions: &defaultMemFuncs,
		},
	}
}

func (m *Component) Handlers() map[string]interface{} {
	return map[string]interface{}{
		"chat":         m.handleChatCommand,
		"chat_reset":   m.handleResetCommand,
		"setprompt":    m.handleSetPromptCommand,
		"clearprompt":  m.handleClearPromptCommand,
		"geminimodels": m.handleModelsCommand,
	}
}

func (m *Component) handleResetCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	key := m.getSessionKey(i.GuildID, i.ChannelID, i.Member.User.ID)

	m.sessionsMu.Lock()
	delete(m.sessions, key)
	m.sessionsMu.Unlock()

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "Your conversation history has been cleared.",
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

func (m *Component) handleSetPromptCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	options := i.ApplicationCommandData().Options
	prompt := options[0].StringValue()

	channelID := "GLOBAL"
	if len(options) > 1 {
		channelID = options[1].ChannelValue(nil).ID
	}

	_, err := m.db.DB.Exec(
		"INSERT INTO gemini_prompts (guild_id, channel_id, prompt) VALUES (?, ?, ?) ON CONFLICT(guild_id, channel_id) DO UPDATE SET prompt=excluded.prompt",
		i.GuildID, channelID, prompt,
	)

	var response string
	if err != nil {
		log.Printf("Failed to set prompt: %v", err)
		response = "Failed to save the pre-prompt."
	} else {
		// Clear history so the new prompt takes effect immediately for everyone
		m.clearSessionsForContext(i.GuildID, channelID)
		if channelID == "GLOBAL" {
			response = fmt.Sprintf("Successfully set the server-wide pre-prompt to:\n> %s", prompt)
		} else {
			response = fmt.Sprintf("Successfully set the pre-prompt for <#%s> to:\n> %s", channelID, prompt)
		}
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: response,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

func (m *Component) handleClearPromptCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	options := i.ApplicationCommandData().Options
	channelID := "GLOBAL"
	if len(options) > 0 {
		channelID = options[0].ChannelValue(nil).ID
	}

	_, err := m.db.DB.Exec("DELETE FROM gemini_prompts WHERE guild_id = ? AND channel_id = ?", i.GuildID, channelID)

	var response string
	if err != nil {
		log.Printf("Failed to clear prompt: %v", err)
		response = "Failed to clear the pre-prompt."
	} else {
		m.clearSessionsForContext(i.GuildID, channelID)
		if channelID == "GLOBAL" {
			response = "Successfully cleared the server-wide pre-prompt."
		} else {
			response = fmt.Sprintf("Successfully cleared the pre-prompt for <#%s>.", channelID)
		}
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: response,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

// clearSessionsForContext removes cached sessions so they can be recreated with the new system instructions
func (m *Component) clearSessionsForContext(guildID, channelID string) {
	m.sessionsMu.Lock()
	defer m.sessionsMu.Unlock()

	for key := range m.sessions {
		parts := strings.Split(key, ":")
		if len(parts) != 3 {
			continue
		}
		sessionGuild := parts[0]
		sessionChannel := parts[1]

		if sessionGuild == guildID {
			if channelID == "GLOBAL" || sessionChannel == channelID {
				delete(m.sessions, key)
			}
		}
	}
}

func (m *Component) handleModelsCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// Acknowledge immediately in case listing takes time
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsEphemeral,
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// List models
	pager, err := m.client.Models.List(ctx, nil)
	if err != nil {
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: strPtr("Failed to fetch models from the Gemini API."),
		})
		return
	}

	var builder strings.Builder
	builder.WriteString("**Available Gemini Models:**\n")

	count := 0
	for _, model := range pager.Items {
		// Only show models that can generate content (to reduce noise)
		canGenerate := false
		for _, action := range model.SupportedActions {
			if action == "generateContent" {
				canGenerate = true
				break
			}
		}

		if canGenerate {
			name := strings.TrimPrefix(model.Name, "models/")
			builder.WriteString(fmt.Sprintf("- `%s`: %s\n", name, model.DisplayName))
			count++
		}
	}

	if count == 0 {
		builder.WriteString("No suitable generation models found.")
	}

	// Make sure we under the 2k char limit
	response := builder.String()
	if len(response) > 2000 {
		response = response[:1996] + "..."
	}

	s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: strPtr(response),
	})
}

func (m *Component) handleChatCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// Acknowledge the interaction immediately to prevent the 3-second timeout
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	options := i.ApplicationCommandData().Options
	prompt := options[0].StringValue()

	key := m.getSessionKey(i.GuildID, i.ChannelID, i.Member.User.ID)
	response, err := m.generateResponse(key, prompt, i.GuildID, i.ChannelID)

	if err != nil {
		log.Printf("Gemini generation error: %v", err)
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: strPtr("Sorry, I encountered an error communicating with the AI. Please try again later."),
		})
		return
	}

	runes := []rune(response)
	var chunks []string
	for i := 0; i < len(runes); i += 4000 {
		end := i + 4000
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[i:end]))
	}

	for idx, chunk := range chunks {
		embed := &discordgo.MessageEmbed{
			Description: chunk,
			Color:       0x00A5FF, // Google Cloud Blueish
		}

		if idx == 0 {
			embed.Author = &discordgo.MessageEmbedAuthor{
				Name:    i.Member.User.Username,
				IconURL: i.Member.User.AvatarURL(""),
			}

			title := prompt
			if len([]rune(title)) > 250 {
				title = string([]rune(title)[:247]) + "..."
			}
			embed.Title = title
		}

		if idx == len(chunks)-1 {
			embed.Footer = &discordgo.MessageEmbedFooter{
				Text: "Powered by Gemini",
			}
			embed.Timestamp = time.Now().Format(time.RFC3339)
		}

		if idx == 0 {
			s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
				Embeds: &[]*discordgo.MessageEmbed{embed},
			})
		} else {
			s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
				Embeds: []*discordgo.MessageEmbed{embed},
			})
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
		s.ChannelMessageSend(mc.ChannelID, "How can I help you today?")
		return
	}

	// Send typing indicator
	s.ChannelTyping(mc.ChannelID)

	key := m.getSessionKey(mc.GuildID, mc.ChannelID, mc.Author.ID)
	response, err := m.generateResponse(key, prompt, mc.GuildID, mc.ChannelID)

	if err != nil {
		log.Printf("Gemini generation error: %v", err)
		s.ChannelMessageSendReply(mc.ChannelID, "Sorry, I encountered an error. Please try again later.", mc.Reference())
		return
	}

	if len(response) > 2000 {
		response = response[:1996] + "..."
	}

	s.ChannelMessageSendReply(mc.ChannelID, response, mc.Reference())
}

// getSessionKey creates a unique thread key. If in DMs (no guild), uses channel+user.
func (m *Component) getSessionKey(guildID, channelID, userID string) string {
	return fmt.Sprintf("%s:%s:%s", guildID, channelID, userID)
}

func (m *Component) getPrePrompt(guildID, channelID string) string {
	var prompt string

	// Check channel specific prompt first
	err := m.db.DB.QueryRow("SELECT prompt FROM gemini_prompts WHERE guild_id = ? AND channel_id = ?", guildID, channelID).Scan(&prompt)
	if err == nil {
		return prompt
	}

	// Fall back to server wide prompt
	err = m.db.DB.QueryRow("SELECT prompt FROM gemini_prompts WHERE guild_id = ? AND channel_id = 'GLOBAL'", guildID).Scan(&prompt)
	if err == nil {
		return prompt
	}

	return ""
}

func (m *Component) generateResponse(key string, prompt string, guildID string, channelID string) (string, error) {
	m.sessionsMu.Lock()
	session, exists := m.sessions[key]

	systemPrompt := m.getPrePrompt(guildID, channelID)

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

func strPtr(s string) *string {
	return &s
}
