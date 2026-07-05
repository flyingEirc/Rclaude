package config

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
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
	// DefaultPTYBinary is empty on purpose: an unset pty.binary makes the
	// server spawn the user's interactive login shell (see ptyhost.LoginShell)
	// so the passthrough is a working terminal rather than a server-pinned
	// tool. Set pty.binary explicitly to pin a program (e.g. claude/codex).
	DefaultPTYBinary                 = ""
	DefaultPTYWorkspaceRoot          = "/workspace"
	DefaultPTYFrameMaxBytes    int64 = 64 * 1024
	DefaultPTYGracefulShutdown       = 5 * time.Second
	DefaultPTYAttachQPS              = 1
	DefaultPTYAttachBurst            = 3
	DefaultPTYStdinBPS         int64 = 1 << 20
	DefaultPTYStdinBurst       int64 = 256 * 1024
	DefaultAuditTable                = "file_audit_log"
	DefaultAuditQueueSize            = 256
	DefaultStartupMaxRetries         = 3
	DefaultStartupRetryDelay         = time.Second
)

var (
	DefaultPTYEnvPassthrough    = []string{"TERM", "LANG", "LC_ALL", "LC_CTYPE", "PATH", "HOME", "SHELL", "CLAUDE_CONFIG_DIR"}
	ErrEmptyServerAddress       = errors.New("config: server.address is required")
	ErrWorkspacePathNotAbs      = errors.New("config: workspace.path must be absolute")
	ErrEmptyListen              = errors.New("config: listen is required")
	ErrEmptyTokens              = errors.New("config: auth.tokens must contain at least one entry")
	ErrMountpointNotAbs         = errors.New("config: fuse.mountpoint must be absolute")
	ErrNegativeReadRate         = errors.New("config: rate_limit.read_bytes_per_sec must be >= 0")
	ErrNegativeWriteRate        = errors.New("config: rate_limit.write_bytes_per_sec must be >= 0")
	ErrPTYWorkspaceRootNotAbs   = errors.New("config: pty.workspace_root must be absolute")
	ErrPTYFrameMaxBytesNegative = errors.New("config: pty.frame_max_bytes must be > 0")
	ErrPTYRateLimitNegative     = errors.New("config: pty.ratelimit values must be >= 0")
	ErrAuditDriverInvalid       = errors.New("config: audit.driver must be one of sqlite/mysql/postgres")
	ErrAuditDSNRequired         = errors.New("config: audit.dsn is required when audit.enabled is true")
	ErrAuditTableInvalid        = errors.New("config: audit.table may only contain letters, digits and underscores")
	ErrStartupRetriesNegative   = errors.New("config: startup.max_retries must be >= 0")
	ErrStartupDelayNegative     = errors.New("config: startup.retry_delay must be >= 0")

	auditTablePattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
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

// LogConfig 控制日志级别、格式与落盘位置。
// 日志始终写入本地文件（默认 JSON），不输出到终端；
// Dir 为空时使用 logx.DefaultDir()（~/.rclaude/logs）。
type LogConfig struct {
	Level      string `mapstructure:"level"`
	Format     string `mapstructure:"format"`
	Dir        string `mapstructure:"dir"`
	MaxSizeMB  int    `mapstructure:"max_size_mb"`
	MaxBackups int    `mapstructure:"max_backups"`
	MaxAgeDays int    `mapstructure:"max_age_days"`
}

type RateLimitConfig struct {
	ReadBytesPerSec  int64 `mapstructure:"read_bytes_per_sec"`
	WriteBytesPerSec int64 `mapstructure:"write_bytes_per_sec"`
}

type DaemonConfig struct {
	Server       ServerEndpoint  `mapstructure:"server"`
	Workspace    Workspace       `mapstructure:"workspace"`
	PTY          DaemonPTYConfig `mapstructure:"pty"`
	Log          LogConfig       `mapstructure:"log"`
	RateLimit    RateLimitConfig `mapstructure:"rate_limit"`
	Audit        AuditConfig     `mapstructure:"audit"`
	Startup      StartupConfig   `mapstructure:"startup"`
	SelfWriteTTL time.Duration   `mapstructure:"self_write_ttl"`
}

// StartupConfig 控制统一入口（rclaude）启动阶段的事件总线重试策略。
// MaxRetries 指初始尝试之外允许的重试次数（总尝试数 = 1 + MaxRetries）；
// RetryDelay 是收到重试通知后再次尝试前的等待时间。
type StartupConfig struct {
	MaxRetries int           `mapstructure:"max_retries"`
	RetryDelay time.Duration `mapstructure:"retry_delay"`
}

// AuditConfig controls persistence of remote file-operation records into a
// local database for after-the-fact auditing.
type AuditConfig struct {
	Enabled   bool   `mapstructure:"enabled"`
	Driver    string `mapstructure:"driver"`
	DSN       string `mapstructure:"dsn"`
	Table     string `mapstructure:"table"`
	QueueSize int    `mapstructure:"queue_size"`
}

type DaemonPTYConfig struct {
	FrameMaxBytes int64 `mapstructure:"frame_max_bytes"`
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

type PTYRateLimitConfig struct {
	AttachQPS   int   `mapstructure:"attach_qps"`
	AttachBurst int   `mapstructure:"attach_burst"`
	StdinBPS    int64 `mapstructure:"stdin_bps"`
	StdinBurst  int64 `mapstructure:"stdin_burst"`
}

type PTYConfig struct {
	Binary                  string             `mapstructure:"binary"`
	Args                    []string           `mapstructure:"args"`
	WorkspaceRoot           string             `mapstructure:"workspace_root"`
	EnvPassthrough          []string           `mapstructure:"env_passthrough"`
	FrameMaxBytes           int64              `mapstructure:"frame_max_bytes"`
	GracefulShutdownTimeout time.Duration      `mapstructure:"graceful_shutdown_timeout"`
	RateLimit               PTYRateLimitConfig `mapstructure:"ratelimit"`
}

type ServerConfig struct {
	Listen             string         `mapstructure:"listen"`
	Auth               AuthConfig     `mapstructure:"auth"`
	FUSE               FUSEConfig     `mapstructure:"fuse"`
	Cache              CacheConfig    `mapstructure:"cache"`
	Prefetch           PrefetchConfig `mapstructure:"prefetch"`
	PTY                PTYConfig      `mapstructure:"pty"`
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
	if c.PTY.FrameMaxBytes <= 0 {
		return ErrPTYFrameMaxBytesNegative
	}
	if err := c.validateStartup(); err != nil {
		return err
	}
	return c.validateAudit()
}

func (c *DaemonConfig) validateStartup() error {
	if c.Startup.MaxRetries < 0 {
		return ErrStartupRetriesNegative
	}
	if c.Startup.RetryDelay < 0 {
		return ErrStartupDelayNegative
	}
	return nil
}

func (c *DaemonConfig) validateAudit() error {
	a := &c.Audit
	if a.Table == "" {
		a.Table = DefaultAuditTable
	}
	if a.QueueSize <= 0 {
		a.QueueSize = DefaultAuditQueueSize
	}
	if !a.Enabled {
		return nil
	}
	if !IsSupportedAuditDriver(a.Driver) {
		return ErrAuditDriverInvalid
	}
	if strings.TrimSpace(a.DSN) == "" {
		return ErrAuditDSNRequired
	}
	if !auditTablePattern.MatchString(a.Table) {
		return ErrAuditTableInvalid
	}
	return nil
}

// IsSupportedAuditDriver reports whether driver names one of the audit
// database backends understood by pkg/audit.
func IsSupportedAuditDriver(driver string) bool {
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case "sqlite", "sqlite3", "mysql", "postgres", "postgresql", "pgsql":
		return true
	default:
		return false
	}
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
	if err := c.validatePTY(); err != nil {
		return err
	}
	return nil
}

func (c *ServerConfig) validatePTY() error {
	if !isAbsolutePTYWorkspaceRoot(c.PTY.WorkspaceRoot) {
		return ErrPTYWorkspaceRootNotAbs
	}
	if c.PTY.FrameMaxBytes <= 0 {
		return ErrPTYFrameMaxBytesNegative
	}
	if hasNegativePTYRateLimit(c.PTY.RateLimit) {
		return ErrPTYRateLimitNegative
	}
	return nil
}

func loadYAML(path string, out any) error {
	v := viper.NewWithOptions(viper.KeyDelimiter("::"))
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	v.SetEnvPrefix(envPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer("::", "_", ".", "_"))
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
		PTY: DaemonPTYConfig{
			FrameMaxBytes: DefaultPTYFrameMaxBytes,
		},
		Audit: AuditConfig{
			Table:     DefaultAuditTable,
			QueueSize: DefaultAuditQueueSize,
		},
		Startup: StartupConfig{
			MaxRetries: DefaultStartupMaxRetries,
			RetryDelay: DefaultStartupRetryDelay,
		},
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
		PTY: PTYConfig{
			Binary:                  DefaultPTYBinary,
			WorkspaceRoot:           DefaultPTYWorkspaceRoot,
			EnvPassthrough:          append([]string(nil), DefaultPTYEnvPassthrough...),
			FrameMaxBytes:           DefaultPTYFrameMaxBytes,
			GracefulShutdownTimeout: DefaultPTYGracefulShutdown,
			RateLimit: PTYRateLimitConfig{
				AttachQPS:   DefaultPTYAttachQPS,
				AttachBurst: DefaultPTYAttachBurst,
				StdinBPS:    DefaultPTYStdinBPS,
				StdinBurst:  DefaultPTYStdinBurst,
			},
		},
	}
}

func isAbsolutePTYWorkspaceRoot(workspaceRoot string) bool {
	return filepath.IsAbs(workspaceRoot) || path.IsAbs(workspaceRoot)
}

func hasNegativePTYRateLimit(rateLimit PTYRateLimitConfig) bool {
	return rateLimit.AttachQPS < 0 ||
		rateLimit.AttachBurst < 0 ||
		rateLimit.StdinBPS < 0 ||
		rateLimit.StdinBurst < 0
}
