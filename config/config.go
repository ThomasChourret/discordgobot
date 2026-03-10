package config

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

// Config holds all the environment variables for the bot
type Config struct {
	DiscordToken string
	GeminiAPIKey string
	GeminiModel  string
}

// Load reads the .env file and validates required variables
func Load() *Config {
	// Ignore the error if .env is missing. It is expected when running inside a Docker container
	// since environment variables are injected directly.
	_ = godotenv.Load()

	discordToken := os.Getenv("DISCORD_TOKEN")
	if discordToken == "" {
		log.Fatal("Fatal: DISCORD_TOKEN environment variable is required")
	}

	geminiKey := os.Getenv("GEMINI_API_KEY")
	// Note: Gemini key might not be strictly required if the module is disabled,
	// but for simplicity in this architecture, we usually supply it globally if we plan to use it.
	if geminiKey == "" {
		log.Println("Warning: GEMINI_API_KEY environment variable is missing. Gemini module may fail.")
	}

	geminiModel := os.Getenv("GEMINI_MODEL")
	if geminiModel == "" {
		geminiModel = "gemma-3-27b-it"
	}

	return &Config{
		DiscordToken: discordToken,
		GeminiAPIKey: geminiKey,
		GeminiModel:  geminiModel,
	}
}
