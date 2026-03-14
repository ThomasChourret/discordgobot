package music

import (
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
	"github.com/thomaschourret/discordgobot/db"
)

type Song struct {
	Title      string
	URL        string
	Path       string
	Cleanup    func()
	Requester  string
	mu         sync.Mutex
	downloadCh chan struct{} // Closed when download finishes
}

type GuildState struct {
	sync.Mutex
	Queue            []*Song
	IsPlaying        bool
	Session          *discordgo.Session
	VoiceConn        *discordgo.VoiceConnection
	TextChannelID    string // The channel where playback messages go
	AllowedChannelID string // The restricted channel for commands (persistent config)
	CurrentSnippet   *Song
	SkipChannel      chan bool
}

type Component struct {
	sync.Mutex
	db      *db.DBWrapper
	States  map[string]*GuildState
	enabled bool
}

func NewModule(database *db.DBWrapper) *Component {
	return &Component{
		db:     database,
		States: make(map[string]*GuildState),
	}
}

func (m *Component) Enable() {
	m.Lock()
	defer m.Unlock()
	m.enabled = true
}

func (m *Component) Disable() {
	m.Lock()
	defer m.Unlock()
	m.enabled = false
}

func (m *Component) IsEnabled() bool {
	m.Lock()
	defer m.Unlock()
	return m.enabled
}

func (m *Component) Name() string {
	return "Music"
}

func (m *Component) Description() string {
	return "Allows users to play music from YouTube in voice channels."
}

func (m *Component) Init(session *discordgo.Session) error {
	m.Lock()
	m.States = make(map[string]*GuildState)
	m.Unlock()

	// 1. Initialize DB table for persistent music config
	_, err := m.db.DB.Exec(`
		CREATE TABLE IF NOT EXISTS music_config (
			guild_id TEXT PRIMARY KEY,
			allowed_channel_id TEXT NOT NULL
		);
	`)
	if err != nil {
		return fmt.Errorf("failed to create music_config table: %w", err)
	}

	// 2. Load existing configs into memory
	rows, err := m.db.DB.Query("SELECT guild_id, allowed_channel_id FROM music_config")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var gID, cID string
			if err := rows.Scan(&gID, &cID); err == nil {
				state := m.getGuildState(gID)
				state.Lock()
				state.AllowedChannelID = cID
				state.Unlock()
			}
		}
	}

	// 3. Install the FFmpeg shim for compatibility with FFmpeg 7+
	err = m.setupFFmpegWrapper()
	if err != nil {
		log.Printf("[Music] Warning: failed to setup FFmpeg wrapper: %v", err)
	}

	// 4. Silence dca library stats parsing spam
	dca.Logger = log.New(&dcaLogFilter{}, "", 0)

	return nil
}

func (m *Component) RegisterCommands() []*discordgo.ApplicationCommand {
	return []*discordgo.ApplicationCommand{
		{
			Name:        "music",
			Description: "Music playback commands",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "play",
					Description: "Play a song from YouTube",
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "url",
							Description: "YouTube URL",
							Required:    true,
						},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "skip",
					Description: "Skip the current song",
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "stop",
					Description: "Stop playback and clear the queue",
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "queue",
					Description: "Show the current queue",
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "config",
					Description: "Configure music command channel restriction",
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionChannel,
							Name:        "channel",
							Description: "The only channel where music commands will work (leave empty to reset)",
							Required:    false,
							ChannelTypes: []discordgo.ChannelType{
								discordgo.ChannelTypeGuildText,
							},
						},
					},
				},
			},
		},
	}
}

func (m *Component) Handlers() map[string]interface{} {
	return map[string]interface{}{
		"music": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			if i.Type != discordgo.InteractionApplicationCommand {
				return
			}

			data := i.ApplicationCommandData()
			if data.Name != "music" {
				return
			}

			subCommand := data.Options[0]

			// Global restriction check
			state := m.getGuildState(i.GuildID)
			state.Lock()
			allowedID := state.AllowedChannelID
			state.Unlock()

			if allowedID != "" && i.ChannelID != allowedID && subCommand.Name != "config" {
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: fmt.Sprintf("❌ Music commands are restricted to <#%s>", allowedID),
						Flags:   discordgo.MessageFlagsEphemeral,
					},
				})
				return
			}

			switch subCommand.Name {
			case "play":
				m.handlePlay(s, i)
			case "skip":
				m.handleSkip(s, i)
			case "stop":
				m.handleStop(s, i)
			case "queue":
				m.handleQueue(s, i)
			case "config":
				m.handleConfig(s, i)
			}
		},
	}
}

func (m *Component) getGuildState(guildID string) *GuildState {
	m.Lock()
	defer m.Unlock()
	state, ok := m.States[guildID]
	if !ok {
		state = &GuildState{
			Queue:       make([]*Song, 0),
			SkipChannel: make(chan bool),
		}
		m.States[guildID] = state
	}
	return state
}

func (m *Component) handlePlay(s *discordgo.Session, i *discordgo.InteractionCreate) {
	url := i.ApplicationCommandData().Options[0].Options[0].StringValue()
	guildID := i.GuildID
	userID := i.Member.User.ID

	// Defer reply to give us time to download if needed
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	// Find the user's voice channel
	voiceChannelID := ""
	g, err := s.State.Guild(guildID)
	if err == nil {
		for _, vs := range g.VoiceStates {
			if vs.UserID == userID {
				voiceChannelID = vs.ChannelID
				break
			}
		}
	}

	if voiceChannelID == "" {
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: m.ptr("❌ You must be in a voice channel to use this command."),
		})
		return
	}

	// Fetch song metadata first (fast)
	title, err := getSongTitle(url)
	if err != nil {
		log.Printf("[Music] Failed to get song title: %v", err)
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: m.ptr("❌ Failed to fetch video info. Is the URL correct?"),
		})
		return
	}

	state := m.getGuildState(guildID)
	state.Lock()
	state.Session = s
	state.TextChannelID = i.ChannelID
	song := &Song{
		Title:     title,
		URL:       url,
		Requester: i.Member.User.Username,
	}
	state.Queue = append(state.Queue, song)
	isPlaying := state.IsPlaying

	// If already playing and this is the only song in the queue (the "next" one), pre-download it immediately
	if isPlaying && len(state.Queue) == 1 {
		go m.preDownloadNext(song)
	}
	state.Unlock()

	s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: m.ptr(fmt.Sprintf("✅ Added **%s** to the queue!", title)),
	})

	if !isPlaying {
		go m.playLoop(guildID, voiceChannelID)
	}
}

func (m *Component) handleSkip(s *discordgo.Session, i *discordgo.InteractionCreate) {
	state := m.getGuildState(i.GuildID)
	state.Lock()
	isPlaying := state.IsPlaying
	state.Unlock()

	if !isPlaying {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ Nothing is playing right now.",
			},
		})
		return
	}

	select {
	case state.SkipChannel <- true:
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "⏭️ Skipping current song...",
			},
		})
	default:
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ Skip already requested or no skip listener.",
			},
		})
	}
}

func (m *Component) handleStop(s *discordgo.Session, i *discordgo.InteractionCreate) {
	state := m.getGuildState(i.GuildID)
	state.Lock()
	// Cleanup any pre-downloaded files in the queue
	for _, song := range state.Queue {
		if song.Cleanup != nil {
			song.Cleanup()
		}
	}
	state.Queue = make([]*Song, 0)
	vc := state.VoiceConn
	state.Unlock()

	if vc != nil {
		vc.Disconnect()
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "🛑 Playback stopped and queue cleared.",
		},
	})
}

func (m *Component) handleQueue(s *discordgo.Session, i *discordgo.InteractionCreate) {
	state := m.getGuildState(i.GuildID)
	state.Lock()
	queue := state.Queue
	state.Unlock()

	if len(queue) == 0 {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "📜 The queue is empty.",
			},
		})
		return
	}

	msg := "📜 **Current Queue:**\n"
	for idx, song := range queue {
		msg += fmt.Sprintf("%d. **%s** (Requested by %s)\n", idx+1, song.Title, song.Requester)
		if idx >= 9 {
			if len(queue) > 10 {
				msg += fmt.Sprintf("...and %d more", len(queue)-10)
			}
			break
		}
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: msg,
		},
	})
}

func (m *Component) handleConfig(s *discordgo.Session, i *discordgo.InteractionCreate) {
	options := i.ApplicationCommandData().Options[0].Options
	guildID := i.GuildID

	var channelID string
	if len(options) > 0 {
		channelID = options[0].ChannelValue(s).ID
	}

	state := m.getGuildState(guildID)
	state.Lock()
	state.AllowedChannelID = channelID
	state.Unlock()

	var err error
	if channelID != "" {
		_, err = m.db.DB.Exec("INSERT INTO music_config (guild_id, allowed_channel_id) VALUES (?, ?) ON CONFLICT(guild_id) DO UPDATE SET allowed_channel_id=excluded.allowed_channel_id", guildID, channelID)
	} else {
		_, err = m.db.DB.Exec("DELETE FROM music_config WHERE guild_id = ?", guildID)
	}

	response := "✅ Music command restriction removed."
	if channelID != "" {
		response = fmt.Sprintf("✅ Music commands are now restricted to <#%s>.", channelID)
	}

	if err != nil {
		log.Printf("[Music] DB error saving config: %v", err)
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

func (m *Component) playLoop(guildID, voiceChannelID string) {
	state := m.getGuildState(guildID)

	state.Lock()
	state.IsPlaying = true
	s := state.Session
	state.Unlock()

	defer func() {
		state.Lock()
		state.IsPlaying = false
		state.Unlock()
	}()

	for {
		state.Lock()
		if len(state.Queue) == 0 {
			if state.VoiceConn != nil {
				state.VoiceConn.Disconnect()
				state.VoiceConn = nil
			}
			state.Unlock()
			return
		}

		song := state.Queue[0]
		state.Queue = state.Queue[1:]

		// Trigger download for the NEXT song if available
		if len(state.Queue) > 0 {
			go m.preDownloadNext(state.Queue[0])
		}
		state.Unlock()

		// Join voice
		vc, err := s.ChannelVoiceJoin(guildID, voiceChannelID, false, true)
		if err != nil {
			log.Printf("[Music] Failed to join voice: %v", err)
			s.ChannelMessageSend(state.TextChannelID, fmt.Sprintf("❌ Failed to join voice channel for **%s**", song.Title))
			continue
		}
		state.Lock()
		state.VoiceConn = vc
		state.Unlock()

		// Wait for ready
		start := time.Now()
		for !vc.Ready && time.Since(start) < 5*time.Second {
			time.Sleep(100 * time.Millisecond)
		}

		if !vc.Ready {
			log.Printf("[Music] Voice connection timeout for %s", guildID)
			s.ChannelMessageSend(state.TextChannelID, "❌ Voice connection timeout.")
			continue
		}

		// Ensure the current song is downloaded
		m.ensureSongDownloaded(song)
		if song.Path == "" {
			s.ChannelMessageSend(state.TextChannelID, fmt.Sprintf("❌ Failed to download **%s**", song.Title))
			continue
		}

		// Play
		s.ChannelMessageSend(state.TextChannelID, fmt.Sprintf("🎵 Now playing: **%s**", song.Title))
		m.playAudio(vc, song.Path, state.SkipChannel)
		if song.Cleanup != nil {
			song.Cleanup()
		}

		// Check if we should continue
		state.Lock()
		if state.VoiceConn == nil {
			state.Unlock()
			return
		}
		state.Unlock()
	}
}

func (m *Component) preDownloadNext(song *Song) {
	song.mu.Lock()
	if song.Path != "" || song.downloadCh != nil {
		song.mu.Unlock()
		return
	}
	song.downloadCh = make(chan struct{})
	song.mu.Unlock()

	log.Printf("[Music] Pre-downloading: %s", song.Title)
	path, cleanup, err := m.downloadAudio(song.URL)

	song.mu.Lock()
	if err == nil {
		song.Path = path
		song.Cleanup = cleanup
	}
	close(song.downloadCh)
	song.mu.Unlock()
}

func (m *Component) ensureSongDownloaded(song *Song) {
	song.mu.Lock()
	if song.Path != "" {
		song.mu.Unlock()
		return
	}

	// If download already triggered, wait for it
	if song.downloadCh != nil {
		ch := song.downloadCh
		song.mu.Unlock()
		log.Printf("[Music] Waiting for pre-download of %s", song.Title)
		<-ch
		return
	}

	// Otherwise start download now
	song.downloadCh = make(chan struct{})
	song.mu.Unlock()

	log.Printf("[Music] Downloading now (no pre-download): %s", song.Title)
	path, cleanup, err := m.downloadAudio(song.URL)
	song.mu.Lock()
	if err == nil {
		song.Path = path
		song.Cleanup = cleanup
	}
	close(song.downloadCh)
	song.mu.Unlock()
}

func (m *Component) playAudio(vc *discordgo.VoiceConnection, path string, skipChan chan bool) {
	opts := *dca.StdEncodeOptions
	opts.RawOutput = true
	opts.Bitrate = 96
	opts.Application = "lowdelay"
	opts.Volume = 256

	encoding, err := dca.EncodeFile(path, &opts)
	if err != nil {
		log.Printf("[Music] DCA encode error: %v", err)
		return
	}
	defer encoding.Cleanup()

	time.Sleep(250 * time.Millisecond)

	vc.Speaking(true)
	defer vc.Speaking(false)

	done := make(chan error)
	stream := dca.NewStream(encoding, vc, done)

	select {
	case err := <-done:
		// Silence "unexpected EOF" which happens when dca's reader is closed during a skip
		if err != nil && err != io.EOF && !strings.Contains(err.Error(), "unexpected EOF") {
			log.Printf("[Music] Stream error: %v", err)
		}
	case <-skipChan:
		log.Printf("[Music] Skipping...")
		stream.Finished()
	}
}

func getSongTitle(url string) (string, error) {
	cmd := exec.Command("yt-dlp", "--get-title", "--no-playlist", url)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (m *Component) downloadAudio(youtubeURL string) (string, func(), error) {
	tmpDir, err := os.MkdirTemp("", "music-")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	cleanupFunc := func() {
		os.RemoveAll(tmpDir)
	}

	outputTemplate := tmpDir + "/audio.%(ext)s"

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

	entries, err := os.ReadDir(tmpDir)
	if err != nil || len(entries) == 0 {
		cleanupFunc()
		return "", nil, fmt.Errorf("no audio file found")
	}

	audioPath := tmpDir + "/" + entries[0].Name()
	return audioPath, cleanupFunc, nil
}

func (m *Component) setupFFmpegWrapper() error {
	realFFmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return fmt.Errorf("ffmpeg not found in PATH: %w", err)
	}

	absReal, _ := filepath.Abs(realFFmpeg)
	if strings.Contains(absReal, "ffmpeg-shim") {
		return nil
	}

	tmpDir, err := os.MkdirTemp("", "ffmpeg-shim-music-")
	if err != nil {
		return fmt.Errorf("failed to create temp dir for ffmpeg shim: %w", err)
	}

	shimPath := filepath.Join(tmpDir, "ffmpeg")
	shimContent := fmt.Sprintf(`#!/bin/bash
REAL_FFMPEG="%s"
ARGS=()
VOL=""
AF=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -vol) VOL="$2"; shift 2 ;;
    -af) if [[ -n "$AF" ]]; then AF="$AF,"; fi; AF="$AF$2"; shift 2 ;;
    *) ARGS+=("$1"); shift ;;
  esac
done
if [[ -n "$VOL" ]]; then
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

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", tmpDir+string(os.PathListSeparator)+oldPath)
	return nil
}

type dcaLogFilter struct{}

func (f *dcaLogFilter) Write(p []byte) (n int, err error) {
	msg := string(p)
	if strings.Contains(msg, "Error parsing ffmpeg stats") {
		return len(p), nil
	}
	return os.Stderr.Write(p)
}

func (m *Component) ptr(s string) *string {
	return &s
}
