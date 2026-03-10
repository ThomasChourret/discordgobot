# Modular Discord Bot

A highly modular and containerized Discord bot built in **Go (Golang)**. This system is designed around a core plugin architecture, allowing you to seamlessly enable and configure separate features as isolated modules.

## 🚀 Key Features
- **Fast & Lightweight:** Written purely in Go. High performance and very low memory footprint.
- **Pluggable Architecture:** Features operate as modular plugins. They handle registering their own specific dependencies, commands, and events.
- **Container-Ready:** Deployment is designed natively for Podman/Docker using a multi-stage Docker build, generating an ultra-small image.
- **Modern Discord UI:** Uses slash commands, buttons, and embed messages instead of traditional prefix commands or reactions.

---

## 🧩 Included Modules

### 1. `getrole` (Interactive Role Menus)
Allows admins to create clean, responsive Role Menus using Discord message components (buttons). Users click the buttons on a beautifully formatted embed message attached to receive or remove roles automatically.  
*(See [getrole documentation](modules/getrole/README.md))*

### 2. `gemini` (Conversational AI via Commands)
Integrates Google's Gemini models for rich conversational AI using Slash Commands (`/chat`). Features include persistent short-term thread memory, user context, and tunable system "personas" (prompts) using `/setprompt`.  
*(See [gemini documentation](modules/gemini/README.md))*

### 3. `geminipersona` (Natural Mention AI)
Integrates Google's Gemini models natively into channel conversations. By simply `@mentioning` the bot, it will read your prompt and reply gracefully. Uniquely, this module disables system instructions so it remains compatible with models that reject system prompts.  
*(See [geminipersona documentation](modules/geminipersona/README.md))*

---

## 🛠️ Installation & Setup

### Prerequisites
- [Go (1.20+)](https://go.dev/) for local builds.
- [Docker](https://www.docker.com/) or [Podman](https://podman.io/) for containerized deployment.
- A Discord Bot Token (get it from the [Discord Developer Portal](https://discord.com/developers/applications)).
- A Gemini API Key (get it from [Google AI Studio](https://aistudio.google.com/app/apikey)).

### Configuration (.env)
1. Copy the example environment file:
   ```bash
   cp .env.example .env
   ```
2. Edit the `.env` file and fill in your keys:
   ```env
   # Your Discord Bot Token
   DISCORD_TOKEN=your_token_here
   
   # Your Google Gemini API Key
   GEMINI_API_KEY=your_gemini_api_key_here
   
   # The Gemini model to use (default: gemma-3-27b-it)
   GEMINI_MODEL=gemma-3-27b-it
   ```

### Running Locally (Go)
To run the bot locally via the Go compiler:
```bash
go mod tidy
go run .
```
*(The bot will automatically create a `./data` directory to store its SQLite database).*

### Running via Docker / Podman
To easily deploy the bot in a containerized environment (recommended for long-term production):
```bash
docker compose up -d
# or using podman:
podman-compose up -d
```

---

## 📁 Architecture & File Structure

```text
discordbot/
├── core/                   # The Core Module Manager routing Discord events
├── db/                     # Lightweight local DB Wrapper (SQLite)
├── config/                 # Loads and centralizes `.env` configuration
├── modules/                # Pluggable Feature Folders
│   ├── gemini/             # Gemini Slash Command & Persona module
│   ├── geminipersona/      # Gemini Mention handler module
│   └── getrole/            # Interactive Embedded Role Menu module
├── data/                   # (Created at runtime) Stores the SQLite DB
├── architecture.md         # Detailed architectural design specifications
├── main.go                 # Bootstrapper. Central entrypoint.
├── compose.yaml            # Docker-compose production layout
└── Dockerfile              # Multi-stage optimized Docker build
```

Read more about the design decisions in the [`architecture.md`](architecture.md) file.
