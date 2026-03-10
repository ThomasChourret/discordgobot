# Gemini Persona Module

The **Gemini Persona** module integrates Google's Gemini AI directly into Discord channel conversations by responding when the bot is explicitly `@mentioned`.

## Features

- **Natural Mention Responses**: Users can chat with the AI simply by mentioning the bot in any channel (e.g. `@botname Hello!`). The bot will reply directly to the message.
- **No System Instructions**: This module passes prompts to the Gemini API without any System Instructions, making it compatible with models that reject System Prompts (like the "thinking" models).
- **In-Memory Threaded History**: The module keeps track of conversation history in memory on a per-channel/per-user basis so the model remembers the context of the conversation.

## Configuration

This module utilizes the following environment variables:
- `GEMINI_API_KEY` (Required): The API key to access Google's Gemini models.
- `GEMINI_MODEL` (Optional): The specific model to use for chat interactions (defaults to `gemma-3-27b-it`).

## Commands

This module does not expose any specific slash commands. For commands such as `/chat` or setting pre-prompts, see the `gemini` module.

## Behavior
- The module automatically splits and sends multiple messages in sequence if the AI's response exceeds Discord's 2000 character limit.
- Conversations are stored in an in-memory session cache. Threads that are inactive for more than 24 hours are automatically garbage-collected to prevent memory leaks. Also, all history is lost when the bot restarts.
