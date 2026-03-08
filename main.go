package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/robfig/cron/v3"
)

type Config struct {
	Schedule             string
	ScanPath             string
	Args                 string
	Action               string
	ActionArgs           string
	WebhookURL           string
	NotifyOnCompletion   bool
	NotifyOnlyDuplicates bool
}

// allowedActions restricts FCLONES_ACTION to official fclones subcommands
// to prevent command injection via environment variables.
// See: https://github.com/pkolaczk/fclones#usage
var allowedActions = map[string]bool{
	actionGroup: true,
	"remove":    true,
	"link":      true,
	"dedupe":    true,
}

const actionGroup = "group"

// healthFile is created on startup and after successful scans,
// removed on scan failure. The "health" subcommand checks its existence
// for Docker healthchecks without requiring an HTTP server or open port.
const healthFile = "/tmp/.healthy"

var (
	mu         sync.Mutex
	currentJob *exec.Cmd
)

const (
	// Fixed container paths — configured via volume mounts, not env vars.
	scanDir  = "/scandir"
	cacheDir = "/cache"
)

func main() {
	// CLI health probe for Docker healthcheck (distroless has no curl/wget).
	// Checks for a marker file instead of making an HTTP request — no port needed.
	if len(os.Args) > 1 && os.Args[1] == "health" {
		if _, err := os.Stat(healthFile); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}

	cfg := loadConfig()

	// Ensure cache directory exists and is writable
	// fclones will create /fclones subdirectory inside this path
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		log.Printf("WARNING: Failed to create cache directory %s: %v", cacheDir, err)
	} else {
		// Portable write check to ensure fclones can write to it
		testFile := filepath.Join(cacheDir, ".write_test")
		if f, err := os.Create(testFile); err != nil {
			log.Printf("WARNING: Cache directory %s is not writable by current user (uid=%d). Caching may fail.", cacheDir, os.Getuid())
		} else {
			f.Close()
			os.Remove(testFile)
			log.Printf("Cache directory %s verified writable. fclones will use %s/fclones/", cacheDir, cacheDir)
		}
	}

	c := cron.New()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	_, err := c.AddFunc(cfg.Schedule, func() {
		runFclonesJob(ctx, &cfg)
	})
	if err != nil {
		stop()
		log.Fatalf("Invalid cron schedule '%s': %v", cfg.Schedule, err)
	}

	// Mark healthy on startup, remove on exit
	setHealthy(true)
	defer os.Remove(healthFile)

	c.Start()
	discordStatus := "disabled"
	if cfg.WebhookURL != "" {
		discordStatus = "configured"
	}
	log.Printf("Container started (uid=%d). Schedule: %s | Target: %s | Discord: %s",
		os.Getuid(), cfg.Schedule, cfg.ScanPath, discordStatus)

	log.Println("Triggering startup scan...")
	go runFclonesJob(ctx, &cfg)

	<-ctx.Done()
	stop()

	log.Printf("Shutting down (%v)", context.Cause(ctx))
	c.Stop()
}

func loadConfig() Config {
	action := getEnv("FCLONES_ACTION", actionGroup)
	if !allowedActions[action] {
		log.Fatalf("Invalid FCLONES_ACTION '%s': must be one of: group, remove, link, dedupe", action)
	}

	args := getEnv("FCLONES_ARGS", "")
	actionArgs := getEnv("FCLONES_ACTION_ARGS", "")
	rejectDangerousArgs(args, "FCLONES_ARGS")
	rejectDangerousArgs(actionArgs, "FCLONES_ACTION_ARGS")

	return Config{
		Schedule:             getEnv("FCLONES_SCHEDULE", "0 */3 * * *"),
		ScanPath:             getEnv("FCLONES_SCAN_PATHS", scanDir),
		Args:                 args,
		Action:               action,
		ActionArgs:           actionArgs,
		WebhookURL:           getEnv("DISCORD_WEBHOOK_URL", ""),
		NotifyOnCompletion:   getEnvBool("DISCORD_NOTIFY_ON_COMPLETION", true),
		NotifyOnlyDuplicates: getEnvBool("DISCORD_NOTIFY_ONLY_IF_DUPLICATES", false),
	}
}

// rejectDangerousArgs blocks fclones flags that could execute arbitrary commands.
func rejectDangerousArgs(raw, envVar string) {
	args, err := parseArgs(raw)
	if err != nil {
		log.Fatalf("Invalid argument syntax in %s: %v", envVar, err)
	}
	for _, arg := range args {
		lower := strings.ToLower(arg)
		if lower == "--command" || strings.HasPrefix(lower, "--command=") {
			log.Fatalf("Dangerous flag '--command' in %s is not allowed", envVar)
		}
	}
}

// setHealthy creates or removes the health marker file.
func setHealthy(healthy bool) {
	if healthy {
		if f, err := os.Create(healthFile); err == nil {
			f.Close()
		}
	} else {
		os.Remove(healthFile)
	}
}

// buildScanArgs constructs the fclones scan command arguments from config.
func buildScanArgs(cfg *Config) ([]string, error) {
	args := []string{actionGroup}
	scanArgs, err := parseArgs(cfg.ScanPath)
	if err != nil {
		return nil, fmt.Errorf("invalid scan path syntax: %w", err)
	}
	args = append(args, scanArgs...)

	if cfg.Args != "" {
		extraArgs, err := parseArgs(cfg.Args)
		if err != nil {
			return nil, fmt.Errorf("invalid FCLONES_ARGS syntax: %w", err)
		}
		args = append(args, extraArgs...)
	}

	// Enable caching - fclones uses $XDG_CACHE_HOME/fclones (or $HOME/.cache/fclones)
	args = append(args, "--cache")
	return args, nil
}

// clearCurrentJob resets the currentJob sentinel under the mutex.
func clearCurrentJob() {
	mu.Lock()
	currentJob = nil
	mu.Unlock()
}

// runFclonesAction executes the post-scan action (remove, link, dedupe) on the report file.
func runFclonesAction(ctx context.Context, cfg *Config, tmpFile string) string {
	if cfg.Action == actionGroup || cfg.Action == "" {
		return ""
	}

	actionCmdArgs := []string{cfg.Action}
	if cfg.ActionArgs != "" {
		extraArgs, err := parseArgs(cfg.ActionArgs)
		if err != nil {
			log.Printf("Invalid FCLONES_ACTION_ARGS syntax: %v", err)
		} else {
			actionCmdArgs = append(actionCmdArgs, extraArgs...)
		}
	}

	log.Printf("Performing action: fclones %s", strings.Join(actionCmdArgs, " "))

	actionCmd := exec.CommandContext(ctx, "fclones", actionCmdArgs...)
	inFile, err := os.Open(tmpFile)
	if err != nil {
		log.Printf("Failed to open report for action: %v", err)
		return ""
	}
	defer inFile.Close()

	actionCmd.Stdin = inFile
	var actionCombined bytes.Buffer
	actionCmd.Stdout = io.MultiWriter(os.Stdout, &actionCombined)
	actionCmd.Stderr = io.MultiWriter(os.Stderr, &actionCombined)

	if err := actionCmd.Run(); err != nil {
		log.Printf("Action failed: %v", err)
		return fmt.Sprintf("\n\n**Action (%s) Failed**:\n%s", cfg.Action, actionCombined.String())
	}
	processedLine := extractProcessedLine(actionCombined.String())
	return fmt.Sprintf("\n\n**Action (%s) Complete**:\n%s", cfg.Action, processedLine)
}

// notifyScanComplete sends a Discord notification with scan results.
func notifyScanComplete(cfg *Config, summary, actionStatus, duplicateList string) {
	if !cfg.NotifyOnCompletion {
		return
	}
	hasDuplicates := duplicateList != "No duplicates found."
	if cfg.NotifyOnlyDuplicates && !hasDuplicates {
		return
	}
	// Truncate list for Discord safely
	if len(duplicateList) > 1600 {
		duplicateList = duplicateList[:1600] + "\n... (truncated)"
	}
	desc := fmt.Sprintf("%s%s\n\n**Duplicates found**:\n```\n%s\n```", summary, actionStatus, duplicateList)
	sendDiscord(cfg.WebhookURL, "fclones Task Complete", desc, 0x2ecc71)
}

func runFclonesJob(ctx context.Context, cfg *Config) {
	mu.Lock()
	if currentJob != nil {
		mu.Unlock()
		log.Println("Job already running, skipping overlapping request.")
		return
	}

	// Mark job as running with a sentinel while holding the lock
	// to prevent TOCTOU races between concurrent goroutines.
	sentinel := &exec.Cmd{}
	currentJob = sentinel
	mu.Unlock() // Now unlock AFTER setting sentinel

	startTime := time.Now()
	log.Printf("Starting scan on: %s", cfg.ScanPath)

	tmpFile := filepath.Join(os.TempDir(), "fclones_report.txt")

	args, err := buildScanArgs(cfg)
	if err != nil {
		log.Printf("%v", err)
		clearCurrentJob()
		return
	}

	log.Printf("Running command: fclones %s", strings.Join(args, " "))

	var errBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, "fclones", args...)

	outFile, err := os.Create(tmpFile)
	if err != nil {
		clearCurrentJob()
		log.Printf("Failed to create temp file: %v", err)
		return
	}

	cmd.Stdout = io.MultiWriter(os.Stdout, outFile)
	cmd.Stderr = io.MultiWriter(os.Stderr, &errBuf)

	mu.Lock()
	currentJob = cmd
	mu.Unlock()

	err = cmd.Run()
	outFile.Close()
	clearCurrentJob()

	duration := time.Since(startTime)

	if ctx.Err() != nil {
		log.Println("Scan interrupted by shutdown signal")
		os.Remove(tmpFile)
		return
	}

	if err != nil {
		errMsg := fmt.Sprintf("Scan failed after %s: %v\nStderr: %s", duration, err, errBuf.String())
		log.Println(errMsg)
		if cfg.NotifyOnCompletion {
			sendDiscord(cfg.WebhookURL, "Scan Failed", errMsg, 0xe74c3c)
		}
		setHealthy(false)
		return
	}

	outputBytes, err := os.ReadFile(tmpFile)
	if err != nil {
		log.Printf("Failed to read output file: %v", err)
		outputBytes = []byte{}
	}
	outputStr := string(outputBytes)

	stats := parseStats(outputStr)
	summary := fmt.Sprintf("**Duration**: %s\n**Redundant Data**: %s\n**Files**: %s groups",
		duration.Round(time.Second), stats.Size, stats.Groups)
	log.Printf("Scan Complete. Duration: %s, Redundant: %s, Groups: %s", duration.Round(time.Second), stats.Size, stats.Groups)

	duplicateList := parseDuplicatesFormatted(outputStr)
	actionStatus := runFclonesAction(ctx, cfg, tmpFile)
	notifyScanComplete(cfg, summary, actionStatus, duplicateList)

	os.Remove(tmpFile)
	setHealthy(true)
}

type FclonesStats struct {
	Groups string
	Size   string
}

func parseStats(output string) FclonesStats {
	stats := FclonesStats{Groups: "0", Size: "0 B"}

	for line := range strings.SplitSeq(output, "\n") {
		switch {
		case strings.HasPrefix(line, "# Redundant:"):
			stats.Size = parseRedundantSize(line)
		case strings.HasPrefix(line, "# Total:"):
			if parts := strings.Fields(line); len(parts) >= 2 && parts[len(parts)-1] == "groups" {
				stats.Groups = parts[len(parts)-2]
			}
		}
	}

	return stats
}

// parseRedundantSize extracts the human-readable size from a "# Redundant:" line.
// Prefers the parenthesized form "(1.2 GB)" over the bare "512 MB" form.
func parseRedundantSize(line string) string {
	if start := strings.Index(line, "("); start != -1 {
		if end := strings.Index(line, ")"); end > start {
			return line[start+1 : end]
		}
	}
	if parts := strings.Fields(line); len(parts) >= 4 {
		return parts[2] + " " + parts[3]
	}
	return "0 B"
}

func parseDuplicatesFormatted(report string) string {
	var result strings.Builder
	filesInGroup := 0

	for line := range strings.SplitSeq(report, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			filesInGroup = 0
			continue
		}

		if strings.Contains(line, ",") && strings.Contains(line, "*") && strings.HasSuffix(line, ":") {
			filesInGroup = 0
			continue
		}

		if filesInGroup == 0 {
			result.WriteString(trimmed + "\n")
		} else {
			result.WriteString("↳ " + trimmed + "\n")
		}

		filesInGroup++
	}

	if result.Len() == 0 {
		return "No duplicates found."
	}

	return result.String()
}

func extractProcessedLine(s string) string {
	var lastNonEmpty string

	for line := range strings.SplitSeq(s, "\n") {
		if strings.Contains(line, "Processed") && strings.Contains(line, "reclaimed") {
			if idx := strings.Index(line, "Processed"); idx != -1 {
				return line[idx:]
			}
			return strings.TrimSpace(line)
		}
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lastNonEmpty = trimmed
		}
	}

	if lastNonEmpty != "" {
		return lastNonEmpty
	}
	return "(No output captured)"
}

type DiscordPayload struct {
	Embeds []DiscordEmbed `json:"embeds"`
}

type DiscordEmbed struct {
	Footer      *DiscordFooter `json:"footer,omitempty"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Color       int            `json:"color"`
}

type DiscordFooter struct {
	Text string `json:"text"`
}

func sendDiscord(webhookURL, title, description string, color int) {
	if webhookURL == "" {
		return
	}

	// Validate webhook URL to prevent SSRF — only allow HTTPS Discord webhook URLs
	parsed, err := url.Parse(webhookURL)
	if err != nil || parsed.Scheme != "https" ||
		(parsed.Host != "discord.com" && parsed.Host != "discordapp.com" &&
			!strings.HasSuffix(parsed.Host, ".discord.com")) {
		host := webhookURL
		if parsed != nil {
			host = parsed.Host
		}
		log.Printf("Rejected non-Discord webhook URL: %s", host)
		return
	}

	payload := DiscordPayload{
		Embeds: []DiscordEmbed{{
			Title:       title,
			Description: description,
			Color:       color,
			Footer:      &DiscordFooter{Text: "fclones-container"},
		}},
	}

	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("Failed to marshal Discord payload: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(data))
	if err != nil {
		log.Printf("Failed to create Discord request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Failed to send Discord notification: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		log.Printf("Discord webhook returned HTTP %d", resp.StatusCode)
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		b, err := strconv.ParseBool(v)
		if err == nil {
			return b
		}
	}
	return fallback
}

// parseArgs splits a string into arguments respecting quotes (single and double).
// Returns an error if quotes are not properly terminated.
func parseArgs(input string) ([]string, error) {
	var args []string
	var current strings.Builder
	inQuote := false
	var quoteChar rune
	escaped := false

	for _, r := range input {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}

		if r == '\\' {
			escaped = true
			continue
		}

		switch {
		case inQuote:
			if r == quoteChar {
				inQuote = false
				quoteChar = 0
			} else {
				current.WriteRune(r)
			}
		case r == '"' || r == '\'':
			inQuote = true
			quoteChar = r
		case r == ' ' || r == '\t':
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if inQuote {
		return nil, fmt.Errorf("unterminated %c quote in: %s", quoteChar, input)
	}
	if escaped {
		return nil, fmt.Errorf("trailing backslash in: %s", input)
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args, nil
}
