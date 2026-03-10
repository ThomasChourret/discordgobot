package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/thomaschourret/discordgobot/config"
	"github.com/thomaschourret/discordgobot/core"
	"github.com/thomaschourret/discordgobot/db"
	"github.com/thomaschourret/discordgobot/modules/gemini"
	"github.com/thomaschourret/discordgobot/modules/geminipersona"
	"github.com/thomaschourret/discordgobot/modules/getrole"

	"github.com/bwmarrin/discordgo"
)

func main() {
	// 1. Load Configuration
	cfg := config.Load()

	// 2. Initialize Database Connection (Lightweight SQLite)
	log.Println("Initializing database...")

	// Create data dir if it doesn't exist (for local runs)
	os.MkdirAll("./data", os.ModePerm)

	database := db.Connect("./data/bot.db")
	defer database.Close()
	log.Println("Database connection established.")

	// 3. Create Discord Session
	dg, err := discordgo.New("Bot " + cfg.DiscordToken)
	if err != nil {
		log.Fatalf("Error creating Discord session: %v", err)
	}

	// We want to receive interaction events and message creates
	dg.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages

	// 4. Initialize Core Module Manager
	manager := core.NewModuleManager(dg)

	// 5. Register Modules
	manager.Register(getrole.NewModule(database))
	manager.Register(gemini.NewModule(cfg.GeminiAPIKey, cfg.GeminiModel, database))
	manager.Register(geminipersona.NewModule(cfg.GeminiAPIKey, cfg.GeminiModel))

	// 6. Open Websocket Connection
	log.Println("Opening connection to Discord...")
	err = dg.Open()
	if err != nil {
		log.Fatalf("Error opening connection: %v", err)
	}

	// 7. Init All Modules & Sync Commands
	// We must do this AFTER dg.Open() because syncing commands requires the bot's User ID,
	// which is populated during the Ready event after opening the connection.
	if err := manager.InitAll(); err != nil {
		log.Fatalf("Failed to initialize modules: %v", err)
	}

	// 8. Wait here until CTRL-C or other term signal is received
	log.Println("Bot is now running. Press CTRL-C to exit.")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc

	log.Println("Shutting down cleanly...")
	dg.Close()
}
