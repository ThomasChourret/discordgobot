# Gemini AI Module

The **Gemini AI** module integrates Google's Gemini large language model into your Discord bot, allowing users to have conversational interactions with the AI.

## Features

- **Conversational Chat**: Users can chat with the AI using slash commands or by mentioning the bot.
- **In-Memory Threaded History**: The module keeps track of conversation history in memory on a per-channel/per-user basis so the model remembers the context of the conversation.
- **Pre-prompts (Personas)**: Admins can configure the AI to act with specific system instructions on a per-server or per-channel basis.

## Configuration

This module utilizes the following environment variables:
- `GEMINI_API_KEY` (Required): The API key to access Google's Gemini models.
- `GEMINI_MODEL` (Optional): The specific model to use for chat interactions (defaults to `gemini-3-27b`).

## Commands

### `/chat`
Send a message and get a response from Gemini.
- **Options**:
  - `prompt` (Required): Your message to the AI.

### `/chat_reset`
Resets the conversation history for the current context (User + Channel + Guild). This effectively clears the bot's memory of your previous messages in that exact context.

### `/setprompt`
Set a system pre-prompt (persona/instruction) for the Gemini AI.
- **Permissions Required**: Manage Channels
- **Options**:
  - `prompt` (Required): The behavior instruction (e.g. "Act like a pirate", "Respond only in JSON").
  - `channel` (Optional): The specific channel to apply this prompt to. If omitted, applies to the whole server.

### `/clearprompt`
Clear the configured system pre-prompt for the Gemini AI.
- **Permissions Required**: Manage Channels
- **Options**:
  - `channel` (Optional): The specific channel to clear the prompt from. If omitted, clears the server-wide prompt.

### `/geminimodels`
Lists all available Gemini models that can be used for text generation. 
- **Permissions Required**: Manage Channels

## Behavior
- The module automatically splits and sends multiple embeds if the response exceeds Discord's character limits.
- Conversations are stored in an in-memory session cache. Threads that are inactive for more than 24 hours are automatically garbage-collected to prevent memory leaks. Also, all history is lost when the bot restarts.
