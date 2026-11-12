package config

import (
	"path/filepath"
	"testing"
)

func TestDefaultsUseDeepSeek(t *testing.T) {
	cfg := Default()
	if cfg.DefaultProvider != "deepseek" {
		t.Fatalf("default provider = %q", cfg.DefaultProvider)
	}
	if cfg.Runtime != "norma" {
		t.Fatalf("default runtime = %q", cfg.Runtime)
	}
	p := cfg.Providers["deepseek"]
	if p.Format != "openai" || p.BaseURL == "" || p.Model == "" {
		t.Fatalf("deepseek preset = %+v", p)
	}
	for _, name := range []string{"openai", "anthropic", "zhipu"} {
		if _, ok := cfg.Providers[name]; !ok {
			t.Fatalf("missing provider %q", name)
		}
	}
}

func TestLoadMergesDefaultsAndEnv(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "env-key")
	path := filepath.Join(t.TempDir(), "config.json")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Providers["deepseek"].APIKey != "env-key" {
		t.Fatalf("env key not applied: %q", cfg.Providers["deepseek"].APIKey)
	}
	if len(cfg.Providers) != 4 {
		t.Fatalf("providers = %d", len(cfg.Providers))
	}
}

func TestSaveLoadAndMask(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Default()
	p := cfg.Providers["deepseek"]
	p.APIKey = "sk-1234567890"
	cfg.Providers["deepseek"] = p
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Providers["deepseek"].APIKey != "sk-1234567890" {
		t.Fatalf("key = %q", loaded.Providers["deepseek"].APIKey)
	}
	if got := MaskedKey("sk-1234567890"); got != "****7890" {
		t.Fatalf("mask = %q", got)
	}
	if _, err := loaded.Resolve("deepseek"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
}

func TestResolveWithoutKey(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("HV_LLM_API_KEY", "")
	cfg := Default()
	if _, err := cfg.Resolve("deepseek"); err == nil {
		t.Fatal("expected error without API key")
	}
}

func TestProviderAliases(t *testing.T) {
	cfg := Default()
	cases := map[string]string{
		"chatgpt": "openai",
		"gpt":     "openai",
		"claude":  "anthropic",
		"glm":     "zhipu",
		"智谱":      "zhipu",
	}
	for alias, want := range cases {
		p, err := cfg.Provider(alias)
		if err != nil {
			t.Fatalf("%s: %v", alias, err)
		}
		if p.Name == "" {
			t.Fatalf("%s resolved to empty provider", alias)
		}
		if (alias == "claude" && p.Format != "anthropic") || (alias == "chatgpt" && p.Format != "openai") {
			t.Fatalf("%s format = %s", alias, p.Format)
		}
		_ = want
	}
}

func TestPresetsAndOrder(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Presets() {
		if p.Key == "" || p.Name == "" || p.Model == "" {
			t.Fatalf("incomplete preset: %+v", p)
		}
		if seen[p.Key] {
			t.Fatalf("duplicate preset key %q", p.Key)
		}
		seen[p.Key] = true
	}
	cfg := Default()
	cfg.ProviderOrder = nil
	cfg.Normalize()
	if len(cfg.ProviderOrder) != len(cfg.Providers) {
		t.Fatalf("order = %v, providers = %d", cfg.ProviderOrder, len(cfg.Providers))
	}
	if cfg.ProviderOrder[0] != "deepseek" {
		t.Fatalf("first provider = %q", cfg.ProviderOrder[0])
	}
}
