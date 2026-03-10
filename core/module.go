package core

import "github.com/bwmarrin/discordgo"

// Module defines the standard interface that all bot features must implement.
type Module interface {
	Name() string
	Description() string
	Init(session *discordgo.Session) error
	Enable()
	Disable()
	IsEnabled() bool
	RegisterCommands() []*discordgo.ApplicationCommand
	Handlers() map[string]interface{}
}
