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
	// gRPC keepalive：客户端与服务端周期性发 HTTP/2 PING，一是保活路径上的
	// NAT/防火墙映射（PTY 可长时间空闲，实测 40~60 分钟会被中间设备掐断），
	// 二是让两端在 Time+Timeout 内探测到死连接并走既有清理路径。
	// PING 逐跳终止：Caddy 前置 TLS 时，客户端 PING 只到 Caddy，服务端 PING
	// 只到 Caddy 回源连接；仅明文直连时两端互相探测。详见
	// docs/reference/grpc-keepalive.md。
	DefaultGRPCKeepaliveTime    = 30 * time.Second
	DefaultGRPCKeepaliveTimeout = 10 * time.Second
	// DefaultGRPCKeepaliveMinTime 是服务端 EnforcementPolicy 允许的客户端
	// PING 最小间隔，必须小于 DefaultGRPCKeepaliveTime：gRPC 默认值为 5 分钟，
	// 不放宽的话明文直连部署会把 30s 一次的客户端 PING 判为滥用并 GOAWAY。
	DefaultGRPCKeepaliveMinTime = 10 * time.Second
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
	ErrPTYPredictInvalid        = errors.New("config: pty.predict must be adaptive, always, or off")
	ErrAuditDriverInvalid       = errors.New("config: audit.driver must be one of sqlite/mysql/postgres")
	ErrAuditDSNRequired         = errors.New("config: audit.dsn is required when audit.enabled is true")
	ErrAuditTableInvalid        = errors.New("config: audit.table may only contain letters, digits and underscores")
	ErrStartupRetriesNegative   = errors.New("config: startup.max_retries must be >= 0")
	ErrStartupDelayNegative     = errors.New("config: startup.retry_delay must be >= 0")

	auditTablePattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
)

// ServerEndpoint 描述 daemon/pty 客户端连接的远端 Server。
type ServerEndpoint struct {
	// Address 是 Server 的 gRPC 地址。明文时形如 "1.2.3.4:9326"；
	// 启用 TLS（见 TLS.Enabled）时应指向 TLS 终止端点，形如 "rclaude.example.com:443"。
	Address string `mapstructure:"address"`
	// Token 是应用层 bearer 鉴权凭据，随 gRPC metadata 发送，与传输层加密正交。
	Token string `mapstructure:"token"`
	// TLS 是可选的传输层 TLS 配置；省略或 Enabled=false 时保持明文 gRPC（默认）。
	TLS ServerTLSConfig `mapstructure:"tls"`
}

// ServerTLSConfig 控制 daemon/pty 客户端到 Server 的 TLS 校验。
// 典型用法是在 Server 前置 Caddy 等 TLS 终止器：Server 二进制保持明文 h2c，
// 由前置组件终止 TLS 并 h2c 回源。Enabled=false 时保持明文 gRPC，与既有行为
// 完全一致。所有字段仅作用于客户端拨号，不影响 Server 端。
// 详见 docs/design/caddy-tls-termination.md 与 deploy/tls/。
type ServerTLSConfig struct {
	// Enabled 为 true 时客户端以 TLS 连接 Server（此时 Address 应为 TLS 端点，
	// 例如 Caddy 监听的 :443）；为 false 时明文连接。
	Enabled bool `mapstructure:"enabled"`
	// ServerName 是 TLS 握手的 SNI 与证书校验用主机名；留空时取 Address 的 host。
	// 当客户端直连源站 IP、但证书签发给某域名时，必须显式设为该域名。
	ServerName string `mapstructure:"server_name"`
	// CAFile 指向自定义根 CA（PEM 文件路径）。对接 Caddy 内部 CA / 自签证书时填写；
	// 公网可信证书（如 Let's Encrypt）留空以使用操作系统根证书。
	CAFile string `mapstructure:"ca_file"`
	// InsecureSkipVerify 跳过证书校验（不校验主机名与信任链）。仅供诊断，
	// 切勿用于生产：开启后 TLS 只提供加密、不再提供服务端身份认证。
	InsecureSkipVerify bool `mapstructure:"insecure_skip_verify"`
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
	// Predict selects the predictive local-echo mode for the attach client:
	// "adaptive" (default, show predictions only on slow links), "always",
	// or "off" (plain passthrough).
	Predict string `mapstructure:"predict"`
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
	if !isValidPTYPredict(c.PTY.Predict) {
		return ErrPTYPredictInvalid
	}
	if err := c.validateStartup(); err != nil {
		return err
	}
	return c.validateAudit()
}

// isValidPTYPredict accepts the predictive-echo modes understood by
// pkg/ptypredict; empty means the adaptive default.
func isValidPTYPredict(mode string) bool {
	switch mode {
	case "", "adaptive", "always", "off":
		return true
	default:
		return false
	}
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
