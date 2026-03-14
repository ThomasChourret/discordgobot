package core

import (
	"log"

	"github.com/bwmarrin/discordgo"
)

// ModuleManager handles registration and routing for all bot modules
type ModuleManager struct {
	Session *discordgo.Session
	Modules []Module

	// mappedHandlers stores interaction/event names to their executable functions
	mappedHandlers map[string]interface{}

	// globalHandlers stores event types to a list of handler functions
	globalHandlers map[string][]interface{}
}

// NewModuleManager creates a new manager instance
func NewModuleManager(s *discordgo.Session) *ModuleManager {
	m := &ModuleManager{
		Session:        s,
		Modules:        []Module{},
		mappedHandlers: make(map[string]interface{}),
		globalHandlers: make(map[string][]interface{}),
	}

	// Register the global InteractionCreate handler
	s.AddHandler(m.handleInteractionCreate)

	// Note: You can add more global routers here (e.g., MessageCreate)
	s.AddHandler(m.handleMessageCreate)
	s.AddHandler(m.handleVoiceStateUpdate)

	s.AddHandler(m.handleReady)

	return m
}

// Register adds a module to the manager
func (m *ModuleManager) Register(mod Module) {
	log.Printf("Registering module: %s - %s", mod.Name(), mod.Description())
	m.Modules = append(m.Modules, mod)
}

// InitAll initializes and enables all registered modules
func (m *ModuleManager) InitAll() error {
	var cmds []*discordgo.ApplicationCommand

	for _, mod := range m.Modules {
		// Initialize
		if err := mod.Init(m.Session); err != nil {
			log.Printf("Failed to init module %s: %v", mod.Name(), err)
			continue
		}

		// Enable by default
		mod.Enable()
		log.Printf("Enabled module: %s", mod.Name())

		// Collect application commands
		modCmds := mod.RegisterCommands()
		if modCmds != nil {
			cmds = append(cmds, modCmds...)
		}

		// Collect handlers
		for k, v := range mod.Handlers() {
			if k == "MessageCreate" || k == "VoiceStateUpdate" {
				m.globalHandlers[k] = append(m.globalHandlers[k], v)
			} else {
				m.mappedHandlers[k] = v
			}
		}
	}

	// Sync commands globally (Note: In dev, syncing globally takes up to 1h. For demo, we do global, but ideally sync to a test guild ID)
	if len(cmds) > 0 {
		log.Printf("Registering %d global application commands...", len(cmds))
		_, err := m.Session.ApplicationCommandBulkOverwrite(m.Session.State.User.ID, "", cmds)
		if err != nil {
			log.Printf("Error registering global commands: %v", err)
		} else {
			log.Println("Global commands successfully registered.")
		}
	}

	return nil
}

// handleReady logs when the bot is fully ready
func (m *ModuleManager) handleReady(s *discordgo.Session, r *discordgo.Ready) {
	log.Printf("Logged in as: %v#%v", s.State.User.Username, s.State.User.Discriminator)
}

// handleInteractionCreate routes interactions (Slash Commands, Buttons, Modals) to registered handlers
func (m *ModuleManager) handleInteractionCreate(s *discordgo.Session, i *discordgo.InteractionCreate) {
	var handlerName string

	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		handlerName = i.ApplicationCommandData().Name
	case discordgo.InteractionMessageComponent:
		handlerName = i.MessageComponentData().CustomID
	case discordgo.InteractionModalSubmit:
		handlerName = i.ModalSubmitData().CustomID
	}

	if handlerName == "" {
		return
	}

	// Try to find an exact match first (e.g., for slash commands)
	if h, ok := m.mappedHandlers[handlerName]; ok {
		if handlerFunc, ok := h.(func(*discordgo.Session, *discordgo.InteractionCreate)); ok {
			handlerFunc(s, i)
			return
		}
	}

	// For MessageComponents (Buttons/Selects), we might have dynamic IDs (e.g., "role_1234").
	// A naive prefix search (e.g., if ID starts with "role_") could be added here if exact match fails.
	for pattern, h := range m.mappedHandlers {
		// Basic check if the ID starts with the pattern (useful for "role_assignment_" buttons)
		if len(handlerName) >= len(pattern) && handlerName[:len(pattern)] == pattern {
			if handlerFunc, ok := h.(func(*discordgo.Session, *discordgo.InteractionCreate)); ok {
				handlerFunc(s, i)
				return
			}
		}
	}

	log.Printf("No handler found for interaction: %s", handlerName)
}

// handleMessageCreate routes plain text messages to all enabled modules that registered a `MessageCreate` handler
func (m *ModuleManager) handleMessageCreate(s *discordgo.Session, mc *discordgo.MessageCreate) {
	// Ignore the bot itself
	if mc.Author.ID == s.State.User.ID {
		return
	}

	// If modules registered "MessageCreate", call them all
	if handlers, ok := m.globalHandlers["MessageCreate"]; ok {
		for _, h := range handlers {
			if handlerFunc, ok := h.(func(*discordgo.Session, *discordgo.MessageCreate)); ok {
				handlerFunc(s, mc)
			}
		}
	}
}

// handleVoiceStateUpdate routes voice state updates to all enabled modules that registered a `VoiceStateUpdate` handler
func (m *ModuleManager) handleVoiceStateUpdate(s *discordgo.Session, vs *discordgo.VoiceStateUpdate) {
	if handlers, ok := m.globalHandlers["VoiceStateUpdate"]; ok {
		for _, h := range handlers {
			if handlerFunc, ok := h.(func(*discordgo.Session, *discordgo.VoiceStateUpdate)); ok {
				handlerFunc(s, vs)
			}
		}
	}
}
