package getrole

import (
	"database/sql"
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
	return "Get-Role"
}

func (m *Component) Description() string {
	return "Allows admins to create interactive messages for users to self-assign roles."
}

func (m *Component) Init(session *discordgo.Session) error {
	// Initialize SQLite table if it doesn't exist
	// We map the custom physical Button ID to the actual Discord Role ID.
	query := `
	CREATE TABLE IF NOT EXISTS role_menus (
		button_id TEXT PRIMARY KEY,
		role_id TEXT NOT NULL,
		guild_id TEXT NOT NULL
	);
	`
	_, err := m.db.DB.Exec(query)
	if err != nil {
		return fmt.Errorf("failed to create role_menus table: %w", err)
	}
	return nil
}

func (m *Component) Enable()  {}
func (m *Component) Disable() {}
func (m *Component) IsEnabled() bool {
	return true
}

func (m *Component) RegisterCommands() []*discordgo.ApplicationCommand {
	// Require manageable channels permission (Admin-ish)
	defaultMemFuncs := int64(discordgo.PermissionManageChannels)

	cmd := []*discordgo.ApplicationCommand{
		{
			Name:                     "rolemenu",
			Description:              "Create an interactive role assignment menu",
			DefaultMemberPermissions: &defaultMemFuncs,
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "title",
					Description: "The title of the embed message",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionRole,
					Name:        "role1",
					Description: "First role option",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "description",
					Description: "Description of the embed message",
					Required:    false,
				},
			},
		},
	}

	// Dynamically append the remaining 22 role options to stay under Discord's 25 option limit
	for i := 2; i <= 23; i++ {
		cmd[0].Options = append(cmd[0].Options, &discordgo.ApplicationCommandOption{
			Type:        discordgo.ApplicationCommandOptionRole,
			Name:        fmt.Sprintf("role%d", i),
			Description: fmt.Sprintf("Role option %d", i),
			Required:    false,
		})
	}

	return cmd
}

func (m *Component) Handlers() map[string]interface{} {
	return map[string]interface{}{
		"rolemenu": m.handleRoleMenuCommand,
		// Using a prefix router. core.manager.go will route any interaction starting with "getrole_" to this function.
		"getrole_": m.handleRoleButtonContext,
	}
}

// handleRoleMenuCommand processes the /rolemenu slash command
func (m *Component) handleRoleMenuCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	options := i.ApplicationCommandData().Options
	optMap := make(map[string]*discordgo.ApplicationCommandInteractionDataOption, len(options))
	for _, opt := range options {
		optMap[opt.Name] = opt
	}

	title := optMap["title"].StringValue()
	var desc string
	if opt, ok := optMap["description"]; ok {
		desc = opt.StringValue()
	}

	var roles []*discordgo.ApplicationCommandInteractionDataOption
	// Collect up to 23 roles
	for j := 1; j <= 23; j++ {
		key := fmt.Sprintf("role%d", j)
		if r, ok := optMap[key]; ok {
			roles = append(roles, r)
		}
	}

	// Build the response message
	embed := &discordgo.MessageEmbed{
		Title: title,
		Color: 0x5865F2, // Discord Blurple
	}

	if desc != "" {
		embed.Description = desc
	}

	// Create buttons and save to DB
	var buttons []discordgo.MessageComponent

	for idx, roleOpt := range roles {
		// roleOpt.Value is the RoleID string
		roleID := roleOpt.RoleValue(s, i.GuildID).ID
		roleName := roleOpt.RoleValue(s, i.GuildID).Name

		// Create a unique button ID for this specific role assignment in this guild
		// Note: Button IDs are limited to 100 chars
		buttonID := fmt.Sprintf("getrole_%s_%s", i.GuildID, roleID)

		// Check if it already exists, if not, store it
		err := m.storeRoleMapping(buttonID, roleID, i.GuildID)
		if err != nil {
			log.Printf("Failed to store role mapping: %v", err)
			continue
		}

		style := discordgo.PrimaryButton
		if idx%2 != 0 {
			style = discordgo.SecondaryButton
		}

		buttons = append(buttons, discordgo.Button{
			Label:    roleName,
			Style:    style,
			CustomID: buttonID,
		})
	}

	// A message can have multiple ActionRows, but we can only stick 5 buttons per row
	var components []discordgo.MessageComponent

	for i := 0; i < len(buttons); i += 5 {
		end := i + 5
		if end > len(buttons) {
			end = len(buttons)
		}

		components = append(components, discordgo.ActionsRow{
			Components: buttons[i:end],
		})
	}

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds:     []*discordgo.MessageEmbed{embed},
			Components: components,
		},
	})

	if err != nil {
		log.Printf("Failed to respond to /rolemenu: %v", err)
	}
}

// handleRoleButtonContext processes button clicks for role assignment
func (m *Component) handleRoleButtonContext(s *discordgo.Session, i *discordgo.InteractionCreate) {
	buttonID := i.MessageComponentData().CustomID

	// Look up the role ID
	var roleID string
	err := m.db.DB.QueryRow("SELECT role_id FROM role_menus WHERE button_id = ?", buttonID).Scan(&roleID)
	if err != nil {
		if err == sql.ErrNoRows {
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "Oops! This button expired or the configuration is missing.",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
		} else {
			log.Printf("DB query error: %v", err)
		}
		return
	}

	// Check if user already has the role
	hasRole := false
	for _, r := range i.Member.Roles {
		if r == roleID {
			hasRole = true
			break
		}
	}

	var responseMsg string
	if hasRole {
		// Remove role
		err = s.GuildMemberRoleRemove(i.GuildID, i.Member.User.ID, roleID)
		if err == nil {
			responseMsg = "Role removed successfully."
		} else {
			responseMsg = "Failed to remove role. I might lack permissions."
			log.Printf("Error removing role: %v", err)
		}
	} else {
		// Add role
		err = s.GuildMemberRoleAdd(i.GuildID, i.Member.User.ID, roleID)
		if err == nil {
			responseMsg = "Role added successfully!"
		} else {
			responseMsg = "Failed to add role. I might lack permissions."
			log.Printf("Error adding role: %v", err)
		}
	}

	// Acknowledge interaction ephemerally
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: responseMsg,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

func (m *Component) storeRoleMapping(buttonID, roleID, guildID string) error {
	_, err := m.db.DB.Exec(
		"INSERT INTO role_menus (button_id, role_id, guild_id) VALUES (?, ?, ?) ON CONFLICT(button_id) DO UPDATE SET role_id=excluded.role_id",
		buttonID, roleID, guildID,
	)
	return err
}
