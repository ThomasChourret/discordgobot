package blindtest

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/jonas747/dca"
	"github.com/thomaschourret/discordgobot/core"
	"github.com/thomaschourret/discordgobot/db"
)

type Component struct {
	db *db.DBWrapper

	// activeGames tracks running blind test loops per guild
	activeGames   map[string]*gameState
	activeGamesMu sync.Mutex
}

type gameState struct {
	stopCh  chan struct{}
	running bool
	// currentArtist holds the artist to guess for the active round
	currentArtist string
	// roundActive is true when a round is in progress and answers are accepted
	roundActive bool
	mu          sync.Mutex
}

// Ensure Component implements core.Module
var _ core.Module = (*Component)(nil)

func NewModule(database *db.DBWrapper) *Component {
	return &Component{
		db:          database,
		activeGames: make(map[string]*gameState),
	}
}

func (m *Component) Name() string {
	return "Blind-Test"
}

func (m *Component) Description() string {
	return "Automatic music blind test game in voice channels."
}

func (m *Component) Init(session *discordgo.Session) error {
	// 1. Create FFmpeg Wrapper for FFmpeg 7+ compatibility (intercept -vol flag)
	err := m.setupFFmpegWrapper()
	if err != nil {
		log.Printf("[BlindTest] Warning: failed to setup FFmpeg wrapper: %v", err)
	}

	// 2. Silence dca library stats parsing spam (FFmpeg 7 format mismatch)
	dca.Logger = log.New(&dcaLogFilter{}, "", 0)

	queries := []string{
		`CREATE TABLE IF NOT EXISTS blindtest_songs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			guild_id TEXT NOT NULL,
			youtube_url TEXT NOT NULL,
			artist TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS blindtest_config (
			guild_id TEXT PRIMARY KEY,
			voice_channel_id TEXT NOT NULL,
			text_channel_id TEXT NOT NULL,
			snippet_duration INTEGER NOT NULL DEFAULT 15,
			round_interval INTEGER NOT NULL DEFAULT 120
		);`,
		`CREATE TABLE IF NOT EXISTS blindtest_scores (
			guild_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			score INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (guild_id, user_id)
		);`,
	}

	for _, query := range queries {
		_, err := m.db.DB.Exec(query)
		if err != nil {
			return fmt.Errorf("failed to create blindtest table: %w", err)
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
			Name:        "blindtest",
			Description: "Manage the blind test music quiz",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "add",
					Description: "Add a song to the playlist",
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "url",
							Description: "YouTube URL of the song",
							Required:    true,
						},
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "artist",
							Description: "Artist name (the answer to guess)",
							Required:    true,
						},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "remove",
					Description: "Remove a song from the playlist",
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionInteger,
							Name:        "id",
							Description: "Song ID to remove",
							Required:    true,
						},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "config",
					Description: "Configure blind test channels and timing",
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionChannel,
							Name:        "voice_channel",
							Description: "Voice channel to play music in",
							Required:    true,
							ChannelTypes: []discordgo.ChannelType{
								discordgo.ChannelTypeGuildVoice,
							},
						},
						{
							Type:        discordgo.ApplicationCommandOptionChannel,
							Name:        "text_channel",
							Description: "Text channel for guesses",
							Required:    true,
							ChannelTypes: []discordgo.ChannelType{
								discordgo.ChannelTypeGuildText,
							},
						},
						{
							Type:        discordgo.ApplicationCommandOptionInteger,
							Name:        "duration",
							Description: "Snippet duration in seconds (default 15)",
							Required:    false,
						},
						{
							Type:        discordgo.ApplicationCommandOptionInteger,
							Name:        "interval",
							Description: "Interval between rounds in seconds (default 120)",
							Required:    false,
						},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "start",
					Description: "Start the blind test game",
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "stop",
					Description: "Stop the blind test game",
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "scores",
					Description: "Show the leaderboard",
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "list",
					Description: "List all songs in the playlist",
				},
			},
			DefaultMemberPermissions: &defaultMemFuncs,
		},
	}
}

func (m *Component) Handlers() map[string]interface{} {
	return map[string]interface{}{
		"blindtest":     m.handleCommand,
		"MessageCreate": m.handleMessageCreate,
	}
}

// handleCommand routes subcommands
func (m *Component) handleCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	subCmd := i.ApplicationCommandData().Options[0]

	switch subCmd.Name {
	case "add":
		m.handleAdd(s, i, subCmd)
	case "remove":
		m.handleRemove(s, i, subCmd)
	case "config":
		m.handleConfig(s, i, subCmd)
	case "start":
		m.handleStart(s, i)
	case "stop":
		m.handleStop(s, i)
	case "scores":
		m.handleScores(s, i)
	case "list":
		m.handleList(s, i)
	}
}

func (m *Component) handleAdd(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	opts := sub.Options
	url := opts[0].StringValue()
	artist := opts[1].StringValue()

	_, err := m.db.DB.Exec("INSERT INTO blindtest_songs (guild_id, youtube_url, artist) VALUES (?, ?, ?)",
		i.GuildID, url, artist)

	response := fmt.Sprintf("✅ Added song by **%s** to the playlist.", artist)
	if err != nil {
		log.Printf("[BlindTest] DB error adding song: %v", err)
		response = "❌ Failed to add song."
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: response,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

func (m *Component) handleRemove(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	songID := sub.Options[0].IntValue()

	res, err := m.db.DB.Exec("DELETE FROM blindtest_songs WHERE id = ? AND guild_id = ?", songID, i.GuildID)
	response := fmt.Sprintf("✅ Removed song #%d.", songID)
	if err != nil {
		response = "❌ Failed to remove song."
	} else {
		rows, _ := res.RowsAffected()
		if rows == 0 {
			response = "❌ Song not found."
		}
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: response,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

func (m *Component) handleConfig(s *discordgo.Session, i *discordgo.InteractionCreate, sub *discordgo.ApplicationCommandInteractionDataOption) {
	opts := sub.Options
	optMap := make(map[string]*discordgo.ApplicationCommandInteractionDataOption)
	for _, o := range opts {
		optMap[o.Name] = o
	}

	voiceID := optMap["voice_channel"].ChannelValue(s).ID
	textID := optMap["text_channel"].ChannelValue(s).ID

	duration := int64(15)
	if d, ok := optMap["duration"]; ok {
		duration = d.IntValue()
	}
	interval := int64(120)
	if iv, ok := optMap["interval"]; ok {
		interval = iv.IntValue()
	}

	_, err := m.db.DB.Exec(
		`INSERT INTO blindtest_config (guild_id, voice_channel_id, text_channel_id, snippet_duration, round_interval)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(guild_id) DO UPDATE SET voice_channel_id=excluded.voice_channel_id, text_channel_id=excluded.text_channel_id, snippet_duration=excluded.snippet_duration, round_interval=excluded.round_interval`,
		i.GuildID, voiceID, textID, duration, interval,
	)

	response := fmt.Sprintf("✅ Blind test configured: voice=<#%s>, text=<#%s>, duration=%ds, interval=%ds", voiceID, textID, duration, interval)
	if err != nil {
		log.Printf("[BlindTest] DB error: %v", err)
		response = "❌ Failed to save configuration."
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: response,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

func (m *Component) handleStart(s *discordgo.Session, i *discordgo.InteractionCreate) {
	m.activeGamesMu.Lock()
	gs, exists := m.activeGames[i.GuildID]
	if exists && gs.running {
		m.activeGamesMu.Unlock()
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "⚠️ Blind test is already running!",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	gs = &gameState{
		stopCh:  make(chan struct{}),
		running: true,
	}
	m.activeGames[i.GuildID] = gs
	m.activeGamesMu.Unlock()

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "🎵 Blind test started! Get ready...",
		},
	})

	go m.gameLoop(s, i.GuildID, gs)
}

func (m *Component) handleStop(s *discordgo.Session, i *discordgo.InteractionCreate) {
	m.activeGamesMu.Lock()
	gs, exists := m.activeGames[i.GuildID]
	m.activeGamesMu.Unlock()

	if !exists || !gs.running {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "⚠️ No blind test is running.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	close(gs.stopCh)

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "🛑 Blind test stopped.",
		},
	})
}

func (m *Component) handleScores(s *discordgo.Session, i *discordgo.InteractionCreate) {
	rows, err := m.db.DB.Query("SELECT user_id, score FROM blindtest_scores WHERE guild_id = ? ORDER BY score DESC LIMIT 10", i.GuildID)
	if err != nil {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ Failed to fetch scores.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}
	defer rows.Close()

	var sb strings.Builder
	sb.WriteString("🏆 **Blind Test Leaderboard**\n\n")
	rank := 1
	found := false
	for rows.Next() {
		var userID string
		var score int
		rows.Scan(&userID, &score)
		medal := ""
		switch rank {
		case 1:
			medal = "🥇"
		case 2:
			medal = "🥈"
		case 3:
			medal = "🥉"
		default:
			medal = fmt.Sprintf("**%d.**", rank)
		}
		sb.WriteString(fmt.Sprintf("%s <@%s> — %d pts\n", medal, userID, score))
		rank++
		found = true
	}

	if !found {
		sb.WriteString("No scores yet! Start a blind test to play.")
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: sb.String(),
		},
	})
}

func (m *Component) handleList(s *discordgo.Session, i *discordgo.InteractionCreate) {
	rows, err := m.db.DB.Query("SELECT id, artist, youtube_url FROM blindtest_songs WHERE guild_id = ? ORDER BY id", i.GuildID)
	if err != nil {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ Failed to fetch songs.",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}
	defer rows.Close()

	var sb strings.Builder
	sb.WriteString("🎵 **Blind Test Playlist**\n\n")
	found := false
	for rows.Next() {
		var id int
		var artist, url string
		rows.Scan(&id, &artist, &url)
		sb.WriteString(fmt.Sprintf("**#%d** — %s (`%s`)\n", id, artist, url))
		found = true
	}

	if !found {
		sb.WriteString("No songs yet! Use `/blindtest add` to add some.")
	}

	content := sb.String()
	if len(content) > 2000 {
		content = content[:1996] + "..."
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

// gameLoop runs the periodic blind test rounds
func (m *Component) gameLoop(s *discordgo.Session, guildID string, gs *gameState) {
	defer func() {
		m.activeGamesMu.Lock()
		gs.running = false
		delete(m.activeGames, guildID)
		m.activeGamesMu.Unlock()
	}()

	// Load config
	var voiceChID, textChID string
	var snippetDuration, roundInterval int
	err := m.db.DB.QueryRow("SELECT voice_channel_id, text_channel_id, snippet_duration, round_interval FROM blindtest_config WHERE guild_id = ?", guildID).
		Scan(&voiceChID, &textChID, &snippetDuration, &roundInterval)
	if err != nil {
		log.Printf("[BlindTest] No config for guild %s: %v", guildID, err)
		s.ChannelMessageSend(textChID, "❌ Blind test not configured. Use `/blindtest config` first.")
		return
	}

	// Run first round immediately, then on a ticker
	m.runRound(s, guildID, gs, voiceChID, textChID, snippetDuration)

	ticker := time.NewTicker(time.Duration(roundInterval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-gs.stopCh:
			return
		case <-ticker.C:
			m.runRound(s, guildID, gs, voiceChID, textChID, snippetDuration)
		}
	}
}

func (m *Component) runRound(s *discordgo.Session, guildID string, gs *gameState, voiceChID, textChID string, snippetDuration int) {
	// Check if anyone is in the voice channel
	guild, err := s.State.Guild(guildID)
	if err != nil {
		log.Printf("[BlindTest] Error getting guild: %v", err)
		return
	}

	hasUsers := false
	for _, vs := range guild.VoiceStates {
		if vs.ChannelID == voiceChID {
			// Skip bots
			member, err := s.GuildMember(guildID, vs.UserID)
			if err == nil && !member.User.Bot {
				hasUsers = true
				break
			}
		}
	}

	if !hasUsers {
		log.Printf("[BlindTest] No users in voice channel, skipping round")
		return
	}

	// Pick a random song
	var songID int
	var youtubeURL, artist string
	err = m.db.DB.QueryRow(
		"SELECT id, youtube_url, artist FROM blindtest_songs WHERE guild_id = ? ORDER BY RANDOM() LIMIT 1",
		guildID,
	).Scan(&songID, &youtubeURL, &artist)
	if err != nil {
		if err == sql.ErrNoRows {
			s.ChannelMessageSend(textChID, "⚠️ No songs in the playlist! Use `/blindtest add` to add some.")
		}
		return
	}

	// Set current artist for answer matching
	gs.mu.Lock()
	gs.currentArtist = artist
	gs.roundActive = true
	gs.mu.Unlock()

	// Announce the round
	s.ChannelMessageSend(textChID, "🎵 **A new song is playing!** Type the artist name to score a point!")

	// Download audio via yt-dlp to a temp file
	audioFile, cleanup, err := downloadAudio(youtubeURL)
	if err != nil {
		log.Printf("[BlindTest] Failed to download audio: %v", err)
		s.ChannelMessageSend(textChID, "❌ Failed to load the song, skipping...")
		gs.mu.Lock()
		gs.roundActive = false
		gs.mu.Unlock()
		return
	}
	defer cleanup()

	// Join voice and play
	vc, err := s.ChannelVoiceJoin(guildID, voiceChID, false, true)
	if err != nil {
		log.Printf("[BlindTest] Failed to join voice: %v", err)
		gs.mu.Lock()
		gs.roundActive = false
		gs.mu.Unlock()
		return
	}

	// Wait for the voice connection to be ready
	log.Printf("[BlindTest] Waiting for voice connection to be ready...")
	ready := false
	for i := 0; i < 20; i++ {
		if vc.Ready {
			ready = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !ready {
		log.Printf("[BlindTest] Voice connection not ready after 5s, aborting")
		vc.Disconnect()
		gs.mu.Lock()
		gs.roundActive = false
		gs.mu.Unlock()
		return
	}
	log.Printf("[BlindTest] Voice connection ready, playing audio...")

	// Play the snippet
	playAudioSnippet(vc, audioFile, snippetDuration)

	// Disconnect from voice
	vc.Disconnect()

	// Wait 30 seconds for answers after snippet ends
	answerTimeout := time.After(30 * time.Second)
	select {
	case <-answerTimeout:
	case <-gs.stopCh:
		gs.mu.Lock()
		gs.roundActive = false
		gs.mu.Unlock()
		return
	}

	// Check if round is still active (nobody guessed)
	gs.mu.Lock()
	wasActive := gs.roundActive
	gs.roundActive = false
	gs.mu.Unlock()

	if wasActive {
		s.ChannelMessageSend(textChID, fmt.Sprintf("⏰ Time's up! The artist was **%s**.", artist))
	}
}

// handleMessageCreate checks messages for correct answers
func (m *Component) handleMessageCreate(s *discordgo.Session, mc *discordgo.MessageCreate) {
	if mc.Author.Bot {
		return
	}

	m.activeGamesMu.Lock()
	gs, exists := m.activeGames[mc.GuildID]
	m.activeGamesMu.Unlock()

	if !exists {
		return
	}

	gs.mu.Lock()
	if !gs.roundActive || gs.currentArtist == "" {
		gs.mu.Unlock()
		return
	}

	// Load text channel from config to check the message is in the right channel
	var textChID string
	err := m.db.DB.QueryRow("SELECT text_channel_id FROM blindtest_config WHERE guild_id = ?", mc.GuildID).Scan(&textChID)
	if err != nil || mc.ChannelID != textChID {
		gs.mu.Unlock()
		return
	}

	// Check if answer matches (case-insensitive, contained)
	guess := strings.ToLower(strings.TrimSpace(mc.Content))
	answer := strings.ToLower(gs.currentArtist)

	if strings.Contains(guess, answer) || strings.Contains(answer, guess) {
		// Correct guess! Only accept if guess is at least 3 chars to avoid single-letter matches
		if len(guess) < 3 {
			gs.mu.Unlock()
			return
		}

		gs.roundActive = false
		artist := gs.currentArtist
		gs.mu.Unlock()

		// Award point
		m.db.DB.Exec(
			`INSERT INTO blindtest_scores (guild_id, user_id, score) VALUES (?, ?, 1)
			 ON CONFLICT(guild_id, user_id) DO UPDATE SET score = score + 1`,
			mc.GuildID, mc.Author.ID,
		)

		s.ChannelMessageSend(mc.ChannelID, fmt.Sprintf("🎉 **%s** got it! The artist was **%s**! +1 point!", mc.Author.Username, artist))
	} else {
		gs.mu.Unlock()
	}
}

// downloadAudio uses yt-dlp to download the audio to a temp file and returns the path
func downloadAudio(youtubeURL string) (string, func(), error) {
	tmpDir, err := os.MkdirTemp("", "blindtest-")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	cleanupFunc := func() {
		os.RemoveAll(tmpDir)
	}

	outputTemplate := tmpDir + "/audio.%(ext)s"

	log.Printf("[BlindTest] Downloading audio from %s to %s", youtubeURL, tmpDir)

	cmd := exec.Command("yt-dlp",
		"-f", "bestaudio",
		"-x",
		"--audio-format", "m4a",
		"-o", outputTemplate,
		"--no-playlist",
		youtubeURL,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		cleanupFunc()
		return "", nil, fmt.Errorf("yt-dlp failed: %w\nOutput: %s", err, string(output))
	}

	// Find the actual output file
	entries, err := os.ReadDir(tmpDir)
	if err != nil || len(entries) == 0 {
		cleanupFunc()
		return "", nil, fmt.Errorf("no audio file found after download")
	}

	audioPath := tmpDir + "/" + entries[0].Name()
	info, _ := os.Stat(audioPath)
	log.Printf("[BlindTest] Download complete: %s (%d bytes)", audioPath, info.Size())
	return audioPath, cleanupFunc, nil
}

// playAudioSnippet streams audio to the Discord voice connection for the given duration
func playAudioSnippet(vc *discordgo.VoiceConnection, audioPath string, durationSecs int) {
	log.Printf("[BlindTest] Starting audio encoding for file: %s (duration: %ds)", audioPath, durationSecs)

	// Create a copy of the standard options to avoid mutating global state
	opts := *dca.StdEncodeOptions
	opts.RawOutput = true
	opts.Bitrate = 96
	opts.Application = "lowdelay"
	opts.Volume = 256 // Explicitly set to 256 to try and skip the '-vol' flag in dca

	log.Printf("[BlindTest] Encoding options: Bitrate=%d, Vol=%d, App=%s", opts.Bitrate, opts.Volume, opts.Application)

	encoding, err := dca.EncodeFile(audioPath, &opts)
	if err != nil {
		log.Printf("[BlindTest] DCA encode error: %v", err)
		return
	}
	defer encoding.Cleanup()

	// Small delay to let the encoder buffer some frames
	time.Sleep(500 * time.Millisecond)

	err = vc.Speaking(true)
	if err != nil {
		log.Printf("[BlindTest] Speaking error: %v", err)
		return
	}
	defer vc.Speaking(false)

	done := make(chan error)
	stream := dca.NewStream(encoding, vc, done)

	timer := time.NewTimer(time.Duration(durationSecs) * time.Second)
	defer timer.Stop()

	log.Printf("[BlindTest] Streaming audio...")
	select {
	case err := <-done:
		ffmpegLog := encoding.FFMPEGMessages()
		if err != nil && err != io.EOF {
			log.Printf("[BlindTest] Stream finished with error: %v", err)
		} else {
			log.Printf("[BlindTest] Stream finished (EOF or nil)")
		}
		if ffmpegLog != "" {
			log.Printf("[BlindTest] FFmpeg full output: %s", ffmpegLog)
		}
	case <-timer.C:
		log.Printf("[BlindTest] Duration timer reached, stopping playback")
		// Force stop the stream
		stream.Finished()
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// setupFFmpegWrapper creates a temporary shim script to make the dca library
// compatible with FFmpeg 7+ by translating the deprecated -vol flag.
func (m *Component) setupFFmpegWrapper() error {
	realFFmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return fmt.Errorf("ffmpeg not found in PATH: %w", err)
	}

	// Ensure we don't wrap our own wrapper if Init is called again
	absReal, _ := filepath.Abs(realFFmpeg)
	if strings.Contains(absReal, "ffmpeg-shim") {
		return nil
	}

	tmpDir, err := os.MkdirTemp("", "ffmpeg-shim-")
	if err != nil {
		return fmt.Errorf("failed to create temp dir for ffmpeg shim: %w", err)
	}

	shimPath := filepath.Join(tmpDir, "ffmpeg")

	// bash shim that translates -vol X to -af volume=X/256
	shimContent := fmt.Sprintf(`#!/bin/bash
# FFmpeg 7+ Compatibility Shim for dca library
REAL_FFMPEG="%s"
ARGS=()
VOL=""
AF=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    -vol)
      VOL="$2"
      shift 2
      ;;
    -af)
      if [[ -n "$AF" ]]; then AF="$AF,"; fi
      AF="$AF$2"
      shift 2
      ;;
    *)
      ARGS+=("$1")
      shift
      ;;
  esac
done

if [[ -n "$VOL" ]]; then
  # Convert dca volume (256 = 1.0) to ffmpeg filter
  VOL_FILTER=$(awk -v v="$VOL" 'BEGIN {print v/256}')
  if [[ -n "$AF" ]]; then AF="volume=$VOL_FILTER,$AF"; else AF="volume=$VOL_FILTER"; fi
fi

if [[ -n "$AF" ]]; then
  exec "$REAL_FFMPEG" "${ARGS[@]}" -af "$AF"
else
  exec "$REAL_FFMPEG" "${ARGS[@]}"
fi
`, absReal)

	err = os.WriteFile(shimPath, []byte(shimContent), 0755)
	if err != nil {
		return fmt.Errorf("failed to write ffmpeg shim: %w", err)
	}

	// Prepend shim directory to PATH
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", tmpDir+string(os.PathListSeparator)+oldPath)

	log.Printf("[BlindTest] FFmpeg 7 compatibility shim installed at: %s (wrapping %s)", shimPath, absReal)
	return nil
}

// dcaLogFilter is a custom writer to silence annoying stats parsing errors
// from the dca library while keeping other logs.
type dcaLogFilter struct{}

func (f *dcaLogFilter) Write(p []byte) (n int, err error) {
	msg := string(p)
	if strings.Contains(msg, "Error parsing ffmpeg stats") {
		return len(p), nil
	}
	return os.Stderr.Write(p)
}
