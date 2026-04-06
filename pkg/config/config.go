package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

// envPrefix 是 viper 的环境变量前缀。
// 例如 DaemonConfig.Server.Address 对应 RCLAUDE_SERVER_ADDRESS。
const envPrefix = "RCLAUDE"

// 错误集合：调用方应使用 errors.Is 比较。
var (
	// ErrEmptyServerAddress 表示 Daemon 配置缺少 server.address。
	ErrEmptyServerAddress = errors.New("config: server.address is required")
	// ErrWorkspacePathNotAbs 表示 Daemon 配置的 workspace.path 不是绝对路径。
	ErrWorkspacePathNotAbs = errors.New("config: workspace.path must be absolute")
	// ErrEmptyListen 表示 Server 配置缺少 listen。
	ErrEmptyListen = errors.New("config: listen is required")
	// ErrEmptyTokens 表示 Server 配置 auth.tokens 为空。
	ErrEmptyTokens = errors.New("config: auth.tokens must contain at least one entry")
	// ErrMountpointNotAbs 表示 Server 配置 fuse.mountpoint 不是绝对路径。
	ErrMountpointNotAbs = errors.New("config: fuse.mountpoint must be absolute")
)

// ServerEndpoint 是 Daemon 连接 Server 的目标信息。
type ServerEndpoint struct {
	Address string `mapstructure:"address"`
	Token   string `mapstructure:"token"`
}

// Workspace 描述 Daemon 暴露的本地工作区。
type Workspace struct {
	Path    string   `mapstructure:"path"`
	Exclude []string `mapstructure:"exclude"`
}

// LogConfig 是日志相关配置。
type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// DaemonConfig 是 Daemon 端的完整配置。
type DaemonConfig struct {
	Server    ServerEndpoint `mapstructure:"server"`
	Workspace Workspace      `mapstructure:"workspace"`
	Log       LogConfig      `mapstructure:"log"`
}

// AuthConfig 是 Server 端鉴权配置。
type AuthConfig struct {
	// Tokens 把 token 映射到 user_id。
	Tokens map[string]string `mapstructure:"tokens"`
}

// FUSEConfig 是 Server 端 FUSE 挂载配置。
type FUSEConfig struct {
	Mountpoint string `mapstructure:"mountpoint"`
}

// CacheConfig 是 Server 端缓存上限配置。
type CacheConfig struct {
	MaxBytes int64 `mapstructure:"max_bytes"`
}

// ServerConfig 是 Server 端的完整配置。
type ServerConfig struct {
	Listen string      `mapstructure:"listen"`
	Auth   AuthConfig  `mapstructure:"auth"`
	FUSE   FUSEConfig  `mapstructure:"fuse"`
	Cache  CacheConfig `mapstructure:"cache"`
	Log    LogConfig   `mapstructure:"log"`
}

// LoadDaemon 从 YAML 文件加载 Daemon 配置并校验。
func LoadDaemon(path string) (*DaemonConfig, error) {
	var cfg DaemonConfig
	if err := loadYAML(path, &cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// LoadServer 从 YAML 文件加载 Server 配置并校验。
func LoadServer(path string) (*ServerConfig, error) {
	var cfg ServerConfig
	if err := loadYAML(path, &cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate 校验 Daemon 配置；调用方一般通过 LoadDaemon 间接触发。
func (c *DaemonConfig) Validate() error {
	if strings.TrimSpace(c.Server.Address) == "" {
		return ErrEmptyServerAddress
	}
	if !filepath.IsAbs(c.Workspace.Path) {
		return ErrWorkspacePathNotAbs
	}
	return nil
}

// Validate 校验 Server 配置；调用方一般通过 LoadServer 间接触发。
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
	return nil
}

// loadYAML 是公共加载逻辑：从 path 读 YAML 并 unmarshal 到 out。
// 同时配置 viper 的环境变量回退（前缀 RCLAUDE，"." → "_"）。
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
