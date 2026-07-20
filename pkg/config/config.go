package config

import (
	"errors"
	"fmt"
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
	DefaultPTYWorkspaceRoot             = "/workspace"
	DefaultPTYFrameMaxBytes       int64 = 64 * 1024
	DefaultPTYGracefulShutdown          = 5 * time.Second
	DefaultPTYAttachQPS                 = 1
	DefaultPTYAttachBurst               = 3
	DefaultPTYStdinBPS            int64 = 1 << 20
	DefaultPTYStdinBurst          int64 = 256 * 1024
	DefaultAuditTable                   = "file_audit_log"
	DefaultAuditQueueSize               = 256
	DefaultStartupMaxRetries            = 3
	DefaultStartupRetryDelay            = 5 * time.Second
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
	DefaultPTYEnvPassthrough = []string{"TERM", "LANG", "LC_ALL", "LC_CTYPE", "PATH", "HOME", "SHELL", "CLAUDE_CONFIG_DIR"}
	ErrEmptyServerAddress    = errors.New("config: server.address is required")
	ErrEmptyListen           = errors.New("config: listen is required")
	ErrEmptyTokens           = errors.New("config: auth.tokens must contain at least one entry")
	ErrMountpointNotAbs      = errors.New("config: fuse.mountpoint must be absolute")
	ErrAuditDriverInvalid    = errors.New("config: audit.driver must be one of sqlite/mysql/postgres")
	ErrAuditDSNRequired      = errors.New("config: audit.dsn is required when audit.enabled is true")
	ErrAuditTableInvalid     = errors.New("config: audit.table may only contain letters, digits and underscores")

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

// Workspace 只承载同步行为选项。工作区根目录不再来自配置：daemon 必须在
// 项目根目录启动，启动时的 cwd 即工作区根（见 syncer.ResolveWorkspaceRoot）。
type Workspace struct {
	Exclude           []string `mapstructure:"exclude"`
	SensitivePatterns []string `mapstructure:"sensitive_patterns"`
}

// DaemonConfig 只保留必须由用户提供的项：远端 Server 端点、工作区同步选项、
// 审计。日志（全等级 + JSON）、启动重试（3 次 / 5s）等均写死在入口代码里，
// 不再来自配置。
type DaemonConfig struct {
	Server       ServerEndpoint `mapstructure:"server"`
	Workspace    Workspace      `mapstructure:"workspace"`
	Audit        AuditConfig    `mapstructure:"audit"`
	SelfWriteTTL time.Duration  `mapstructure:"self_write_ttl"`
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

// ServerConfig 只保留监听地址、鉴权、FUSE 挂载点与缓存/预取等必须配置项。
// PTY 会话运行环境（工作区根、env 白名单、帧大小、优雅退出、限速）与日志
// （全等级 + JSON）均写死在入口代码里：PTY 工作区根取 FUSE.Mountpoint。
type ServerConfig struct {
	Listen             string         `mapstructure:"listen"`
	Auth               AuthConfig     `mapstructure:"auth"`
	FUSE               FUSEConfig     `mapstructure:"fuse"`
	Cache              CacheConfig    `mapstructure:"cache"`
	Prefetch           PrefetchConfig `mapstructure:"prefetch"`
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
	if c.SelfWriteTTL <= 0 {
		c.SelfWriteTTL = DefaultSelfWriteTTL
	}
	return c.validateAudit()
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
		Audit: AuditConfig{
			Table:     DefaultAuditTable,
			QueueSize: DefaultAuditQueueSize,
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
	}
}
