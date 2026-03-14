# Gemini Persona Module

The **Gemini Persona** module integrates Google's Gemini AI directly into Discord channel conversations by responding when the bot is explicitly `@mentioned`.

## Features

- **Natural Mention Responses**: Users can chat with the AI simply by mentioning the bot in any channel (e.g. `@botname Hello!`). The bot will reply directly to the message.
- **Configurable Pre-prompts**: Admins can set a server-wide persona or behavior guideline.
- **Flexible AI Integration**: Supports both System Instructions and prompt concatenation, making it compatible with all models (including "thinking" models).
- **In-Memory Threaded History**: The module keeps track of conversation history in memory on a per-channel/per-user basis so the model remembers the context of the conversation.

## Configuration

This module utilizes the following environment variables:
- `GEMINI_API_KEY` (Required): The API key to access Google's Gemini models.
- `GEMINI_MODEL` (Optional): The specific model to use for chat interactions (defaults to `gemma-3-27b-it`).
- `GEMINI_PERSONA_USE_SYSTEM_PROMPT` (Optional): Whether to use System Instructions (true) or concatenate the pre-prompt at the start of each user message (false). Defaults to `false`.

## Commands

### `/personaprompt set`
Set a system pre-prompt (persona/instruction) for the Persona module in this server.
- **Permissions Required**: Manage Channels
- **Options**:
  - `prompt` (Required): The behavior instruction.

### `/personaprompt clear`
Clear the configured system pre-prompt for the Persona module in this server.
- **Permissions Required**: Manage Channels

## Behavior
- The module automatically splits and sends multiple messages in sequence if the AI's response exceeds Discord's 2000 character limit.
- Conversations are stored in an in-memory session cache. Threads that are inactive for more than 24 hours are automatically garbage-collected to prevent memory leaks. Also, all history is lost when the bot restarts.
