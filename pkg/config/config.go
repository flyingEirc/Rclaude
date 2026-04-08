package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const (
	envPrefix                           = "RCLAUDE"
	DefaultRequestTimeout               = 10 * time.Second
	DefaultSelfWriteTTL                 = 2 * time.Second
	DefaultOfflineReadOnlyTTL           = 5 * time.Minute
	DefaultPrefetchEnabled              = true
	DefaultPrefetchMaxFileBytes   int64 = 100 * 1024
	DefaultPrefetchMaxFilesPerDir       = 16
)

var (
	ErrEmptyServerAddress  = errors.New("config: server.address is required")
	ErrWorkspacePathNotAbs = errors.New("config: workspace.path must be absolute")
	ErrEmptyListen         = errors.New("config: listen is required")
	ErrEmptyTokens         = errors.New("config: auth.tokens must contain at least one entry")
	ErrMountpointNotAbs    = errors.New("config: fuse.mountpoint must be absolute")
	ErrNegativeReadRate    = errors.New("config: rate_limit.read_bytes_per_sec must be >= 0")
	ErrNegativeWriteRate   = errors.New("config: rate_limit.write_bytes_per_sec must be >= 0")
)

type ServerEndpoint struct {
	Address string `mapstructure:"address"`
	Token   string `mapstructure:"token"`
}

type Workspace struct {
	Path              string   `mapstructure:"path"`
	Exclude           []string `mapstructure:"exclude"`
	SensitivePatterns []string `mapstructure:"sensitive_patterns"`
}

type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

type RateLimitConfig struct {
	ReadBytesPerSec  int64 `mapstructure:"read_bytes_per_sec"`
	WriteBytesPerSec int64 `mapstructure:"write_bytes_per_sec"`
}

type DaemonConfig struct {
	Server       ServerEndpoint  `mapstructure:"server"`
	Workspace    Workspace       `mapstructure:"workspace"`
	Log          LogConfig       `mapstructure:"log"`
	RateLimit    RateLimitConfig `mapstructure:"rate_limit"`
	SelfWriteTTL time.Duration   `mapstructure:"self_write_ttl"`
}

type AuthConfig struct {
	Tokens map[string]string `mapstructure:"tokens"`
}

type FUSEConfig struct {
	Mountpoint string `mapstructure:"mountpoint"`
}

type CacheConfig struct {
	MaxBytes int64 `mapstructure:"max_bytes"`
}

type PrefetchConfig struct {
	Enabled        bool  `mapstructure:"enabled"`
	MaxFileBytes   int64 `mapstructure:"max_file_bytes"`
	MaxFilesPerDir int   `mapstructure:"max_files_per_dir"`
}

type ServerConfig struct {
	Listen             string         `mapstructure:"listen"`
	Auth               AuthConfig     `mapstructure:"auth"`
	FUSE               FUSEConfig     `mapstructure:"fuse"`
	Cache              CacheConfig    `mapstructure:"cache"`
	Prefetch           PrefetchConfig `mapstructure:"prefetch"`
	Log                LogConfig      `mapstructure:"log"`
	RequestTimeout     time.Duration  `mapstructure:"request_timeout"`
	OfflineReadOnlyTTL time.Duration  `mapstructure:"offline_readonly_ttl"`
}

func LoadDaemon(path string) (*DaemonConfig, error) {
	cfg := defaultDaemonConfig()
	if err := loadYAML(path, &cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func LoadServer(path string) (*ServerConfig, error) {
	cfg := defaultServerConfig()
	if err := loadYAML(path, &cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *DaemonConfig) Validate() error {
	if strings.TrimSpace(c.Server.Address) == "" {
		return ErrEmptyServerAddress
	}
	if !filepath.IsAbs(c.Workspace.Path) {
		return ErrWorkspacePathNotAbs
	}
	if c.RateLimit.ReadBytesPerSec < 0 {
		return ErrNegativeReadRate
	}
	if c.RateLimit.WriteBytesPerSec < 0 {
		return ErrNegativeWriteRate
	}
	if c.SelfWriteTTL <= 0 {
		c.SelfWriteTTL = DefaultSelfWriteTTL
	}
	return nil
}

func (c *ServerConfig) Validate() error {
	if strings.TrimSpace(c.Listen) == "" {
		return ErrEmptyListen
	}
	if len(c.Auth.Tokens) == 0 {
		return ErrEmptyTokens
	}
	if !filepath.IsAbs(c.FUSE.Mountpoint) {
		return ErrMountpointNotAbs
	}
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = DefaultRequestTimeout
	}
	return nil
}

func loadYAML(path string, out any) error {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	v.SetEnvPrefix(envPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		return fmt.Errorf("config: read %q: %w", path, err)
	}
	if err := v.Unmarshal(out); err != nil {
		return fmt.Errorf("config: unmarshal %q: %w", path, err)
	}
	return nil
}

func defaultDaemonConfig() DaemonConfig {
	return DaemonConfig{
		SelfWriteTTL: DefaultSelfWriteTTL,
	}
}

func defaultServerConfig() ServerConfig {
	return ServerConfig{
		RequestTimeout:     DefaultRequestTimeout,
		OfflineReadOnlyTTL: DefaultOfflineReadOnlyTTL,
		Prefetch: PrefetchConfig{
			Enabled:        DefaultPrefetchEnabled,
			MaxFileBytes:   DefaultPrefetchMaxFileBytes,
			MaxFilesPerDir: DefaultPrefetchMaxFilesPerDir,
		},
	}
}
