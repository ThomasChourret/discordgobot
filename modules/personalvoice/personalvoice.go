package personalvoice

import (
	"fmt"
	"log"

	"github.com/thomaschourret/discordgobot/core"
	"github.com/thomaschourret/discordgobot/db"

	"github.com/bwmarrin/discordgo"
)

type Component struct {
	db *db.DBWrapper
}

// Ensure Component implements core.Module
var _ core.Module = (*Component)(nil)

func NewModule(database *db.DBWrapper) *Component {
	return &Component{db: database}
}

func (m *Component) Name() string {
	return "Personal-Voice"
}

func (m *Component) Description() string {
	return "Creates temporary personal voice channels when users join a hub channel."
}

func (m *Component) Init(session *discordgo.Session) error {
	// Initialize SQLite tables
	queries := []string{
		`CREATE TABLE IF NOT EXISTS personal_voice_hubs (
			guild_id TEXT NOT NULL,
			channel_id TEXT NOT NULL,
			PRIMARY KEY (guild_id, channel_id)
		);`,
		`CREATE TABLE IF NOT EXISTS personal_channels (
			channel_id TEXT PRIMARY KEY,
			owner_id TEXT NOT NULL,
			guild_id TEXT NOT NULL
		);`,
	}

	for _, query := range queries {
		_, err := m.db.DB.Exec(query)
		if err != nil {
			return fmt.Errorf("failed to create table: %w", err)
		}
	}
	return nil
}

func (m *Component) Enable()  {}
func (m *Component) Disable() {}
func (m *Component) IsEnabled() bool {
	return true
}

func (m *Component) RegisterCommands() []*discordgo.ApplicationCommand {
	defaultMemFuncs := int64(discordgo.PermissionManageChannels)

	return []*discordgo.ApplicationCommand{
		{
			Name:                     "voicehub",
			Description:              "Manage personal voice channel hubs",
			DefaultMemberPermissions: &defaultMemFuncs,
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "set",
					Description: "Set a voice channel as a hub",
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionChannel,
							Name:        "channel",
							Description: "The voice channel to use as a hub",
							Required:    true,
							ChannelTypes: []discordgo.ChannelType{
								discordgo.ChannelTypeGuildVoice,
							},
						},
					},
				},
			},
		},
	}
}

func (m *Component) Handlers() map[string]interface{} {
	return map[string]interface{}{
		"voicehub":         m.handleVoiceHubCommand,
		"VoiceStateUpdate": m.handleVoiceStateUpdate,
	}
}

func (m *Component) handleVoiceHubCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	options := i.ApplicationCommandData().Options[0].Options
	optMap := make(map[string]*discordgo.ApplicationCommandInteractionDataOption, len(options))
	for _, opt := range options {
		optMap[opt.Name] = opt
	}

	channel := optMap["channel"].ChannelValue(s)

	_, err := m.db.DB.Exec(
		"INSERT INTO personal_voice_hubs (guild_id, channel_id) VALUES (?, ?) ON CONFLICT(guild_id, channel_id) DO UPDATE SET channel_id=excluded.channel_id",
		i.GuildID, channel.ID,
	)

	response := "Voice hub set successfully!"
	if err != nil {
		log.Printf("DB error: %v", err)
		response = "Failed to set voice hub."
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: response,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

func (m *Component) handleVoiceStateUpdate(s *discordgo.Session, vs *discordgo.VoiceStateUpdate) {
	// Focus on transitions
	if vs.BeforeUpdate != nil && vs.BeforeUpdate.ChannelID == vs.VoiceState.ChannelID {
		return
	}

	// 1. Check if user joined a hub
	if vs.VoiceState.ChannelID != "" {
		var hubID string
		err := m.db.DB.QueryRow("SELECT channel_id FROM personal_voice_hubs WHERE guild_id = ? AND channel_id = ?", vs.GuildID, vs.VoiceState.ChannelID).Scan(&hubID)
		if err == nil {
			log.Printf("[PersonalVoice] User joined hub, creating channel...")
			m.createPersonalChannel(s, vs.VoiceState)
			// Don't return here, we still need to check if the user left a personal channel that needs cleanup
		}
	}

	// 2. Check if a personal channel became empty
	if vs.BeforeUpdate != nil && vs.BeforeUpdate.ChannelID != "" {
		var channelID string
		err := m.db.DB.QueryRow("SELECT channel_id FROM personal_channels WHERE channel_id = ?", vs.BeforeUpdate.ChannelID).Scan(&channelID)
		if err == nil {
			log.Printf("[PersonalVoice] User left personal channel %s, checking occupancy...", vs.BeforeUpdate.ChannelID)
			// Check if channel is empty
			guild, err := s.State.Guild(vs.GuildID)
			if err != nil {
				log.Printf("[PersonalVoice] Error getting guild state: %v", err)
				return
			}

			count := 0
			for _, state := range guild.VoiceStates {
				if state.ChannelID == vs.BeforeUpdate.ChannelID {
					count++
				}
			}

			log.Printf("[PersonalVoice] Channel %s now has %d users", vs.BeforeUpdate.ChannelID, count)
			if count == 0 {
				m.deletePersonalChannel(s, vs.BeforeUpdate.ChannelID)
			}
		}
	}
}

func (m *Component) createPersonalChannel(s *discordgo.Session, vs *discordgo.VoiceState) {
	user, err := s.User(vs.UserID)
	if err != nil {
		return
	}

	channelName := fmt.Sprintf("%s's channel", user.Username)

	// Get hub channel info to replicate category
	hubChannel, err := s.Channel(vs.ChannelID)
	if err != nil {
		return
	}

	newChannel, err := s.GuildChannelCreateComplex(vs.GuildID, discordgo.GuildChannelCreateData{
		Name:     channelName,
		Type:     discordgo.ChannelTypeGuildVoice,
		ParentID: hubChannel.ParentID,
	})

	if err != nil {
		log.Printf("Failed to create personal channel: %v", err)
		return
	}

	// Move user
	err = s.GuildMemberMove(vs.GuildID, vs.UserID, &newChannel.ID)
	if err != nil {
		log.Printf("Failed to move user: %v", err)
	}

	// Save to DB
	_, err = m.db.DB.Exec("INSERT INTO personal_channels (channel_id, owner_id, guild_id) VALUES (?, ?, ?)", newChannel.ID, vs.UserID, vs.GuildID)
	if err != nil {
		log.Printf("DB error: %v", err)
	}
}

func (m *Component) deletePersonalChannel(s *discordgo.Session, channelID string) {
	_, err := s.ChannelDelete(channelID)
	if err != nil {
		log.Printf("Failed to delete channel %s: %v", channelID, err)
	}

	_, err = m.db.DB.Exec("DELETE FROM personal_channels WHERE channel_id = ?", channelID)
	if err != nil {
		log.Printf("DB error: %v", err)
	}
}
