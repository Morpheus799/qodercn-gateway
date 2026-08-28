package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"qodercn-gateway/internal/deploy"
	"qodercn-gateway/internal/httpapi"
	"qodercn-gateway/internal/remote"
	"qodercn-gateway/internal/service"
)

var utilityOptions struct {
	exportRemoteAuth   string
	exportServerBundle string
	remoteAuthPick     string
}

type fileConfig struct {
	Host           string `json:"host"`
	Port           int    `json:"port"`
	RemoteBaseURL  string `json:"remote_base_url"`
	RemoteAuthFile string `json:"remote_auth_file"`
	RemoteProxyURL string `json:"remote_proxy_url"`
	RemoteVersion  string `json:"remote_version"`
	Model          string `json:"model"`
	SessionMode    string `json:"session_mode"`
	TimeoutSeconds int    `json:"timeout"`
	AuthKeysFile   string `json:"auth_keys_file"`
}

func main() {
	cfg, configPath := loadConfig()
	if handleUtilityCommands(cfg) {
		return
	}
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)

	svc := service.New(cfg)
	warmupCtx, warmupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := svc.Warmup(warmupCtx); err != nil {
		log.Printf("warmup failed: %v", err)
	} else {
		log.Printf("remote warmup completed")
	}
	warmupCancel()

	server := httpapi.NewServer(addr, svc)

	// Inbound auth is opt-in via an auth-keys file; if configured but empty/unreadable
	// we fail closed rather than silently expose the gateway (e.g. behind a public tunnel).
	if cfg.AuthKeysFile != "" {
		keys, err := loadAuthKeys(cfg.AuthKeysFile)
		if err != nil {
			log.Fatalf("load auth keys file %q: %v", cfg.AuthKeysFile, err)
		}
		if len(keys) == 0 {
			log.Fatalf("auth keys file %q has no usable keys; refusing to start with auth enabled but empty", cfg.AuthKeysFile)
		}
		server.SetAuthKeys(keys)
		log.Printf("inbound auth: ENABLED (%d key(s) from %s)", len(keys), cfg.AuthKeysFile)
	} else {
		log.Printf("inbound auth: disabled (no -auth-keys-file); relying on bind host %s", cfg.Host)
	}

	log.Printf("qodercn-gateway listening on http://%s", addr)
	if configPath != "" {
		log.Printf("config file: %s", configPath)
	}

	errCh := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		log.Fatal(err)
	case sig := <-sigCh:
		log.Printf("received %s, shutting down", sig.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Fatal(err)
	}
}

func loadConfig() (service.Config, string) {
	cfg := service.Config{
		Host:        "127.0.0.1",
		Port:        8095,
		Model:       "kmodel",
		SessionMode: service.SessionModeAuto,
		Timeout:     0,
	}

	configPath, configLoaded := resolveConfigPath()
	if configLoaded {
		fileCfg, err := readFileConfig(configPath)
		if err != nil {
			log.Fatalf("load config file %q: %v", configPath, err)
		}
		overlayFileConfig(&cfg, fileCfg)
	}

	overlayEnvConfig(&cfg)

	host := flag.String("host", cfg.Host, "Listen host")
	port := flag.Int("port", cfg.Port, "Listen port")
	remoteBaseURL := flag.String("remote-base-url", cfg.RemoteBaseURL, "Remote QoderCN API base URL")
	remoteAuthFile := flag.String("remote-auth-file", cfg.RemoteAuthFile, "Remote QoderCN credentials.json path; empty reads local login cache")
	remoteProxyURL := flag.String("remote-proxy-url", cfg.RemoteProxyURL, "Explicit proxy URL for Remote API requests, e.g. http://127.0.0.1:7890")
	remoteVersion := flag.String("remote-version", cfg.RemoteVersion, "Remote QoderCN cosy version")
	model := flag.String("model", cfg.Model, "Default model when an API request omits model")
	timeoutSeconds := flag.Int("timeout", int(cfg.Timeout/time.Second), "Per-request timeout in seconds; 0 disables the proxy deadline")
	authKeysFile := flag.String("auth-keys-file", cfg.AuthKeysFile, "Path to an inbound API-key allowlist (one key per line, '#' comments); empty disables inbound auth")
	exportRemoteAuth := flag.String("export-remote-auth", "", "Export portable Remote API credentials.json to the given path and exit")
	exportServerBundle := flag.String("export-server-bundle", "", "Export a server deployment zip containing credentials.json, config, and docker-compose.yml, then exit")
	remoteAuthPick := flag.String("remote-auth-pick", "auto", "Remote login cache pick policy for export: auto, newest, or longest")
	sessionMode := flag.String("session-mode", string(cfg.SessionMode), "Session mode: auto, fresh, reuse")
	config := flag.String("config", valueOr(configPath, filepath.Join(currentDir(), "qodercn-gateway.json")), "Path to JSON config file")
	flag.Parse()

	finalConfigPath := strings.TrimSpace(*config)

	cfg.Host = strings.TrimSpace(*host)
	cfg.Port = *port
	cfg.RemoteBaseURL = strings.TrimSpace(*remoteBaseURL)
	cfg.RemoteAuthFile = strings.TrimSpace(*remoteAuthFile)
	cfg.RemoteProxyURL = strings.TrimSpace(*remoteProxyURL)
	cfg.RemoteVersion = strings.TrimSpace(*remoteVersion)
	cfg.Model = strings.TrimSpace(*model)
	cfg.SessionMode = parseSessionMode(*sessionMode)
	cfg.Timeout = time.Duration(*timeoutSeconds) * time.Second
	cfg.AuthKeysFile = strings.TrimSpace(*authKeysFile)
	utilityOptions.exportRemoteAuth = strings.TrimSpace(*exportRemoteAuth)
	utilityOptions.exportServerBundle = strings.TrimSpace(*exportServerBundle)
	utilityOptions.remoteAuthPick = strings.TrimSpace(*remoteAuthPick)
	if err := remote.ValidateProxyURL(cfg.RemoteProxyURL); err != nil {
		log.Fatal(err)
	}

	if configLoaded {
		configPath = finalConfigPath
	} else {
		configPath = ""
	}

	return cfg, configPath
}

// maxAuthKeyLen bounds an inbound API key's length.
const maxAuthKeyLen = 64

// validateAuthKey rejects inbound keys that could cause client-compatibility
// issues: keys are capped at maxAuthKeyLen and restricted to characters that
// survive HTTP header transport unambiguously across clients — letters, digits,
// and - _ * + =.
func validateAuthKey(key string) error {
	if len(key) > maxAuthKeyLen {
		return fmt.Errorf("key is %d chars; max is %d", len(key), maxAuthKeyLen)
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		ok := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '*' || c == '+' || c == '='
		if !ok {
			return fmt.Errorf("unsupported character %q; allowed: A-Z a-z 0-9 - _ * + =", c)
		}
	}
	return nil
}

// loadAuthKeys reads an inbound API-key allowlist: one key per line, blank lines
// and lines beginning with '#' ignored. Every key must pass validateAuthKey.
func loadAuthKeys(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var keys []string
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := validateAuthKey(line); err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		keys = append(keys, line)
	}
	return keys, nil
}

func handleUtilityCommands(cfg service.Config) bool {
	if utilityOptions.exportRemoteAuth == "" && utilityOptions.exportServerBundle == "" {
		return false
	}
	policy := remote.CredentialPickPolicy(strings.ToLower(valueOr(utilityOptions.remoteAuthPick, string(remote.CredentialPickAuto))))
	switch policy {
	case remote.CredentialPickAuto, remote.CredentialPickNewest, remote.CredentialPickLongest:
	default:
		log.Fatalf("invalid remote auth pick policy %q; expected auto, newest, or longest", utilityOptions.remoteAuthPick)
	}
	if utilityOptions.exportRemoteAuth != "" {
		result, err := deploy.WriteCredentialFile(cfg.RemoteAuthFile, utilityOptions.exportRemoteAuth, policy)
		if err != nil {
			log.Fatalf("export remote auth: %v", err)
		}
		printExportResult("credentials", result)
	}
	if utilityOptions.exportServerBundle != "" {
		host := cfg.Host
		if strings.TrimSpace(host) == "" || host == "127.0.0.1" || host == "localhost" {
			host = "0.0.0.0"
		}
		result, err := deploy.WriteServerBundle(deploy.ServerBundleOptions{
			AuthFile:      cfg.RemoteAuthFile,
			OutputPath:    utilityOptions.exportServerBundle,
			PickPolicy:    policy,
			BaseURL:       cfg.RemoteBaseURL,
			ProxyURL:      cfg.RemoteProxyURL,
			RemoteVersion: cfg.RemoteVersion,
			Host:          host,
			Port:          cfg.Port,
			Model:         cfg.Model,
		})
		if err != nil {
			log.Fatalf("export server bundle: %v", err)
		}
		printExportResult("server bundle", result)
	}
	return true
}

func printExportResult(kind string, result deploy.ServerBundleResult) {
	fmt.Printf("exported %s: %s\n", kind, result.Path)
	fmt.Printf("credential source: %s\n", result.CredentialSrc)
	if result.TokenExpireAt != "" {
		fmt.Printf("token expire at: %s\n", result.TokenExpireAt)
	}
	if result.TokenExpired {
		fmt.Println("warning: exported credential is already expired")
	}
	if result.UserID != "" || result.MachineID != "" {
		fmt.Printf("account: %s / %s\n", result.UserID, result.MachineID)
	}
	fmt.Println("keep the exported file private; it contains login secrets")
}

func resolveConfigPath() (string, bool) {
	if path := strings.TrimSpace(lookupArgValue("--config")); path != "" {
		return path, true
	}
	if path := strings.TrimSpace(os.Getenv("QODERCN_GATEWAY_CONFIG")); path != "" {
		return path, true
	}
	defaultPath := filepath.Join(currentDir(), "qodercn-gateway.json")
	if info, err := os.Stat(defaultPath); err == nil && !info.IsDir() {
		return defaultPath, true
	}
	return defaultPath, false
}

func readFileConfig(path string) (fileConfig, error) {
	var cfg fileConfig
	body, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func overlayFileConfig(dst *service.Config, src fileConfig) {
	if strings.TrimSpace(src.Host) != "" {
		dst.Host = strings.TrimSpace(src.Host)
	}
	if src.Port > 0 {
		dst.Port = src.Port
	}
	if strings.TrimSpace(src.RemoteBaseURL) != "" {
		dst.RemoteBaseURL = strings.TrimSpace(src.RemoteBaseURL)
	}
	if strings.TrimSpace(src.RemoteAuthFile) != "" {
		dst.RemoteAuthFile = strings.TrimSpace(src.RemoteAuthFile)
	}
	if strings.TrimSpace(src.RemoteProxyURL) != "" {
		dst.RemoteProxyURL = strings.TrimSpace(src.RemoteProxyURL)
	}
	if strings.TrimSpace(src.RemoteVersion) != "" {
		dst.RemoteVersion = strings.TrimSpace(src.RemoteVersion)
	}
	if strings.TrimSpace(src.Model) != "" {
		dst.Model = strings.TrimSpace(src.Model)
	}
	if strings.TrimSpace(src.SessionMode) != "" {
		dst.SessionMode = parseSessionMode(src.SessionMode)
	}
	if src.TimeoutSeconds >= 0 {
		dst.Timeout = time.Duration(src.TimeoutSeconds) * time.Second
	}
	if strings.TrimSpace(src.AuthKeysFile) != "" {
		dst.AuthKeysFile = strings.TrimSpace(src.AuthKeysFile)
	}
}

func overlayEnvConfig(dst *service.Config) {
	if value := strings.TrimSpace(os.Getenv("QODERCN_GATEWAY_HOST")); value != "" {
		dst.Host = value
	}
	if value := envInt("QODERCN_GATEWAY_PORT", 0); value > 0 {
		dst.Port = value
	}
	if value := strings.TrimSpace(os.Getenv("QODERCN_REMOTE_BASE_URL")); value != "" {
		dst.RemoteBaseURL = value
	}
	if value := strings.TrimSpace(os.Getenv("QODERCN_REMOTE_AUTH_FILE")); value != "" {
		dst.RemoteAuthFile = value
	}
	if value := strings.TrimSpace(os.Getenv("QODERCN_REMOTE_PROXY_URL")); value != "" {
		dst.RemoteProxyURL = value
	}
	if value := strings.TrimSpace(os.Getenv("QODERCN_REMOTE_VERSION")); value != "" {
		dst.RemoteVersion = value
	}
	if value := strings.TrimSpace(os.Getenv("QODERCN_GATEWAY_MODEL")); value != "" {
		dst.Model = value
	}
	if value := strings.TrimSpace(os.Getenv("QODERCN_GATEWAY_SESSION_MODE")); value != "" {
		dst.SessionMode = parseSessionMode(value)
	}
	if value := envInt("QODERCN_GATEWAY_TIMEOUT_SECONDS", -1); value >= 0 {
		dst.Timeout = time.Duration(value) * time.Second
	}
	if value := strings.TrimSpace(os.Getenv("QODERCN_AUTH_KEYS_FILE")); value != "" {
		dst.AuthKeysFile = value
	}
}

func parseSessionMode(value string) service.SessionMode {
	mode := service.SessionMode(strings.ToLower(strings.TrimSpace(value)))
	switch mode {
	case service.SessionModeAuto, service.SessionModeFresh, service.SessionModeReuse:
		return mode
	default:
		log.Fatalf("invalid session mode %q; expected auto, fresh, or reuse", value)
		return service.SessionModeAuto
	}
}

func lookupArgValue(flagName string) string {
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		if arg == flagName {
			if i+1 < len(os.Args) {
				return os.Args[i+1]
			}
			return ""
		}
		prefix := flagName + "="
		if strings.HasPrefix(arg, prefix) {
			return strings.TrimPrefix(arg, prefix)
		}
	}
	return ""
}

func envInt(key string, fallback int) int {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		if n, err := strconv.Atoi(value); err == nil {
			return n
		}
	}
	return fallback
}

func currentDir() string {
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

func valueOr(value string, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}
