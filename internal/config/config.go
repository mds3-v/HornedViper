package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Provider describes one LLM endpoint. All providers use the same agent loop;
// Format selects Norma's wire adapter (openai or anthropic).
type Provider struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Format      string `json:"format"` // openai | openai-responses | anthropic
	BaseURL     string `json:"base_url"`
	Model       string `json:"model"`
	APIKey      string `json:"api_key"`
}

// Config is the project-level configuration. It is stored in config.json.
type Config struct {
	Runtime         string              `json:"runtime"` // norma | fake
	DefaultProvider string              `json:"default_provider"`
	ProviderOrder   []string            `json:"provider_order,omitempty"`
	Providers       map[string]Provider `json:"providers"`
	ArtifactDir     string              `json:"artifact_dir"`
	SubmitCommand   string              `json:"submit_command"`
}

// ProviderOrder is the stable UI order.
var ProviderOrder = []string{"deepseek", "openai", "anthropic", "zhipu"}

// Default returns the built-in defaults. DeepSeek is the default provider.
func Default() Config {
	return Config{
		Runtime:         "norma",
		DefaultProvider: "deepseek",
		ProviderOrder:   []string{"deepseek", "openai", "anthropic", "zhipu"},
		ArtifactDir:     "artifacts",
		Providers: map[string]Provider{
			"deepseek": {
				Name: "DeepSeek", Description: "纯 API · 聊天补全",
				Format: "openai", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat",
			},
			"openai": {
				Name: "OpenAI Official", Description: "Responses API · 聊天补全",
				Format: "openai", BaseURL: "https://api.openai.com/v1", Model: "gpt-4o",
			},
			"anthropic": {
				Name: "Anthropic Claude", Description: "Anthropic Messages 接口",
				Format: "anthropic", BaseURL: "https://api.anthropic.com", Model: "claude-sonnet-4-20250514",
			},
			"zhipu": {
				Name: "Zhipu GLM", Description: "纯 API · 聊天补全",
				Format: "openai", BaseURL: "https://open.bigmodel.cn/api/paas/v4", Model: "glm-4.5",
			},
		},
	}
}

// Load reads config.json, merges it over defaults, and applies env fallbacks.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return Config{}, err
		}
		applyEnv(&cfg)
		cfg.Normalize()
		return cfg, nil
	}
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	mergeDefaults(&cfg)
	cfg.Normalize()
	applyEnv(&cfg)
	cfg.Normalize()
	return cfg, nil
}

// Save writes config.json atomically with 0600 permissions.
func Save(path string, cfg Config) error {
	cfg.Normalize()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func mergeDefaults(cfg *Config) {
	def := Default()
	if cfg.Providers == nil {
		cfg.Providers = map[string]Provider{}
	}
	for name, p := range def.Providers {
		current, ok := cfg.Providers[name]
		if !ok {
			cfg.Providers[name] = p
			continue
		}
		if current.Name == "" {
			current.Name = p.Name
		}
		if current.Format == "" {
			current.Format = p.Format
		}
		if current.BaseURL == "" {
			current.BaseURL = p.BaseURL
		}
		if current.Model == "" {
			current.Model = p.Model
		}
		if current.Description == "" {
			current.Description = p.Description
		}
		cfg.Providers[name] = current
	}
}

func applyEnv(cfg *Config) {
	envKeys := map[string][]string{
		"deepseek":  {"DEEPSEEK_API_KEY"},
		"openai":    {"OPENAI_API_KEY"},
		"anthropic": {"ANTHROPIC_API_KEY"},
		"zhipu":     {"ZHIPU_API_KEY", "GLM_API_KEY"},
	}
	for name, keys := range envKeys {
		p := cfg.Providers[name]
		if p.APIKey != "" {
			continue
		}
		for _, key := range keys {
			if v := os.Getenv(key); v != "" {
				p.APIKey = v
				break
			}
		}
		cfg.Providers[name] = p
	}
	def := cfg.Providers[cfg.DefaultProvider]
	if def.APIKey == "" {
		if v := os.Getenv("HV_LLM_API_KEY"); v != "" {
			def.APIKey = v
			cfg.Providers[cfg.DefaultProvider] = def
		}
	}
	if v := os.Getenv("HV_LLM_BASE_URL"); v != "" && def.BaseURL == "" {
		def.BaseURL = v
		cfg.Providers[cfg.DefaultProvider] = def
	}
	if v := os.Getenv("HV_LLM_MODEL"); v != "" && def.Model == "" {
		def.Model = v
		cfg.Providers[cfg.DefaultProvider] = def
	}
}

// canonicalProvider maps common aliases to the stable config keys.
func canonicalProvider(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "chatgpt", "gpt", "openai":
		return "openai"
	case "claude", "anthropic":
		return "anthropic"
	case "zhipu", "glm", "智谱":
		return "zhipu"
	case "deepseek":
		return "deepseek"
	default:
		return strings.ToLower(strings.TrimSpace(name))
	}
}

// canonicalFormat maps UI labels and aliases to stable format values.
func canonicalFormat(f string) string {
	switch strings.ToLower(strings.TrimSpace(f)) {
	case "", "openai", "chat", "chat_completions", "chat-completions":
		return "openai"
	case "responses", "openai-responses", "responses_api", "responses-api":
		return "openai-responses"
	case "anthropic", "claude":
		return "anthropic"
	default:
		return strings.ToLower(strings.TrimSpace(f))
	}
}

// Normalize fills empty fields and ensures the default provider exists.
func (c *Config) Normalize() {
	if c.Runtime == "" {
		c.Runtime = "norma"
	}
	c.DefaultProvider = canonicalProvider(c.DefaultProvider)
	if c.DefaultProvider == "" {
		c.DefaultProvider = "deepseek"
	}
	if c.ArtifactDir == "" {
		c.ArtifactDir = "artifacts"
	}
	if c.Providers == nil {
		c.Providers = map[string]Provider{}
	}
	for name, p := range c.Providers {
		p.Format = canonicalFormat(p.Format)
		if def, ok := Default().Providers[name]; ok {
			if p.BaseURL == "" {
				p.BaseURL = def.BaseURL
			}
			if p.Model == "" {
				p.Model = def.Model
			}
			if p.Name == "" {
				p.Name = def.Name
			}
			if p.Description == "" {
				p.Description = def.Description
			}
		}
		if p.Name == "" {
			p.Name = name
		}
		if p.Description == "" {
			p.Description = p.Format + " · " + p.BaseURL
		}
		c.Providers[name] = p
	}
	// Keep a stable display order and make sure every provider is present once.
	seen := map[string]bool{}
	order := make([]string, 0, len(c.Providers))
	effectiveOrder := c.ProviderOrder
	if len(effectiveOrder) == 0 {
		effectiveOrder = ProviderOrder
	}
	for _, name := range effectiveOrder {
		name = canonicalProvider(name)
		if _, ok := c.Providers[name]; ok && !seen[name] {
			order = append(order, name)
			seen[name] = true
		}
	}
	var rest []string
	for name := range c.Providers {
		if !seen[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	c.ProviderOrder = append(order, rest...)
	if _, ok := c.Providers[c.DefaultProvider]; !ok {
		if len(c.ProviderOrder) > 0 {
			c.DefaultProvider = c.ProviderOrder[0]
		} else {
			c.DefaultProvider = "deepseek"
		}
	}
}

// Names returns provider names in the configured display order.
func (c Config) Names() []string {
	c.Normalize()
	return append([]string(nil), c.ProviderOrder...)
}

// Provider returns a provider by name without requiring an API key.
func (c Config) Provider(name string) (Provider, error) {
	c.Normalize()
	if name == "" {
		name = c.DefaultProvider
	}
	name = canonicalProvider(name)
	p, ok := c.Providers[name]
	if !ok {
		return Provider{}, fmt.Errorf("unknown provider %q", name)
	}
	return p, nil
}

// Resolve returns a usable provider by name (empty means default).
func (c Config) Resolve(name string) (Provider, error) {
	c.Normalize()
	if name == "" {
		name = c.DefaultProvider
	}
	p, err := c.Provider(name)
	if err != nil {
		return Provider{}, err
	}
	if strings.TrimSpace(p.APIKey) == "" {
		return Provider{}, fmt.Errorf("provider %q has no API key; set it in config.json or the Settings page", name)
	}
	return p, nil
}

// MaskedKey returns a display-safe API key.
func MaskedKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if len(key) <= 4 {
		return "****"
	}
	return "****" + key[len(key)-4:]
}

// Store is a concurrency-safe config holder.
type Store struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

func NewStore(path string) (*Store, error) {
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}
	return &Store{path: path, cfg: cfg}, nil
}

func (s *Store) Path() string { return s.path }

func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.cfg
	out.Providers = make(map[string]Provider, len(s.cfg.Providers))
	for k, v := range s.cfg.Providers {
		out.Providers[k] = v
	}
	return out
}

func (s *Store) Save(cfg Config) error {
	cfg.Normalize()
	if err := Save(s.path, cfg); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	return nil
}
