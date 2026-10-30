// Package config 负责从环境变量加载网关配置、应用默认值并做启动校验。
//
// 安全约束：
//   - 所有校验错误只引用环境变量名，不回显变量值；
//   - Config.String 对主密钥、客户端 Key、管理员密码统一脱敏；
//   - 主密钥必须是 base64 编码的 32 字节密钥，客户端 Key 至少 24 个字符。
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// DefaultListenAddr 是默认监听地址；默认只绑定本机回环地址。
	DefaultListenAddr = "127.0.0.1:8080"
	// DefaultDataDir 是默认数据目录，数据库与备份都位于其下。
	DefaultDataDir = "data"
	// DefaultCodexContextWindow 是 Codex 模型 manifest 的默认上下文窗口。
	DefaultCodexContextWindow = 128000
	// DefaultMaxAttempts 是单次请求允许尝试的最大渠道数。
	DefaultMaxAttempts = 3
	// DefaultCaptureMaxBytes 是单个请求/响应 body 捕获的默认上限（8 MiB）。
	DefaultCaptureMaxBytes int64 = 8 << 20
)

const (
	// DefaultUpstreamConnectTimeout 是上游 TCP 连接超时。
	DefaultUpstreamConnectTimeout = 10 * time.Second
	// DefaultUpstreamTLSHandshakeTimeout 是上游 TLS 握手超时。
	DefaultUpstreamTLSHandshakeTimeout = 10 * time.Second
	// DefaultUpstreamResponseHeaderTimeout 是等待上游响应头的超时。
	DefaultUpstreamResponseHeaderTimeout = 180 * time.Second
	// DefaultUpstreamStreamIdleTimeout 是流式读取的空闲超时。
	DefaultUpstreamStreamIdleTimeout = 300 * time.Second
	// DefaultUpstreamRequestTimeout 是非流式请求的总超时。
	DefaultUpstreamRequestTimeout = 300 * time.Second
	// DefaultUpstreamStreamTimeout 是流式请求的总超时。
	DefaultUpstreamStreamTimeout = 15 * time.Minute
	// DefaultCapacityWait 是全部候选渠道满载时的最长等待时间。
	DefaultCapacityWait = 2 * time.Second
)

const redacted = "<redacted>"

// 环境变量名集中声明，避免错误信息与文档出现拼写漂移。
const (
	EnvListenAddr                  = "GATEWAY_LISTEN_ADDR"
	EnvDataDir                     = "GATEWAY_DATA_DIR"
	EnvDBPath                      = "GATEWAY_DB_PATH"
	EnvMasterKey                   = "GATEWAY_MASTER_KEY"
	EnvClientKey                   = "GATEWAY_CLIENT_KEY"
	EnvAdminPassword               = "GATEWAY_ADMIN_PASSWORD"
	EnvCookieSecure                = "GATEWAY_COOKIE_SECURE"
	EnvCodexContextWindow          = "GATEWAY_CODEX_CONTEXT_WINDOW"
	EnvUpstreamConnectTimeout      = "GATEWAY_UPSTREAM_CONNECT_TIMEOUT"
	EnvUpstreamTLSHandshakeTimeout = "GATEWAY_UPSTREAM_TLS_TIMEOUT"
	EnvUpstreamHeaderTimeout       = "GATEWAY_UPSTREAM_HEADER_TIMEOUT"
	EnvUpstreamStreamIdleTimeout   = "GATEWAY_UPSTREAM_STREAM_IDLE_TIMEOUT"
	EnvUpstreamRequestTimeout      = "GATEWAY_UPSTREAM_TIMEOUT"
	EnvUpstreamStreamTimeout       = "GATEWAY_UPSTREAM_STREAM_TIMEOUT"
	EnvMaxAttempts                 = "GATEWAY_MAX_ATTEMPTS"
	EnvCapacityWait                = "GATEWAY_CAPACITY_WAIT"
	EnvCaptureMaxBytes             = "GATEWAY_CAPTURE_MAX_BYTES"
)

// ValidationError 描述某个环境变量的校验失败；不包含变量值。
type ValidationError struct {
	Variable string
	Reason   string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("配置 %s 无效：%s", e.Variable, e.Reason)
}

// Config 是网关进程的完整静态配置。
//
// MasterKey 只以字节切片保存；任何日志输出都必须经过 String。
type Config struct {
	ListenAddr         string
	DataDir            string
	DBPath             string
	MasterKey          []byte
	ClientKey          string
	AdminPassword      string
	CookieSecure       bool
	CodexContextWindow int

	UpstreamConnectTimeout        time.Duration
	UpstreamTLSHandshakeTimeout   time.Duration
	UpstreamResponseHeaderTimeout time.Duration
	UpstreamStreamIdleTimeout     time.Duration
	UpstreamRequestTimeout        time.Duration
	UpstreamStreamTimeout         time.Duration
	MaxAttempts                   int
	CapacityWait                  time.Duration
	CaptureMaxBytes               int64
}

// Load 从进程环境变量加载配置。
func Load() (Config, error) {
	return LoadFrom(os.Getenv)
}

// LoadFrom 使用给定的 getenv 函数加载配置，便于测试注入。
//
// 缺失或非法值返回 *ValidationError，错误只引用变量名。
func LoadFrom(getenv func(string) string) (Config, error) {
	if getenv == nil {
		return Config{}, errors.New("config: getenv 不能为空")
	}

	cfg := Config{
		ListenAddr:                    envOrDefault(getenv, EnvListenAddr, DefaultListenAddr),
		DataDir:                       envOrDefault(getenv, EnvDataDir, DefaultDataDir),
		CodexContextWindow:            DefaultCodexContextWindow,
		UpstreamConnectTimeout:        DefaultUpstreamConnectTimeout,
		UpstreamTLSHandshakeTimeout:   DefaultUpstreamTLSHandshakeTimeout,
		UpstreamResponseHeaderTimeout: DefaultUpstreamResponseHeaderTimeout,
		UpstreamStreamIdleTimeout:     DefaultUpstreamStreamIdleTimeout,
		UpstreamRequestTimeout:        DefaultUpstreamRequestTimeout,
		UpstreamStreamTimeout:         DefaultUpstreamStreamTimeout,
		MaxAttempts:                   DefaultMaxAttempts,
		CapacityWait:                  DefaultCapacityWait,
		CaptureMaxBytes:               DefaultCaptureMaxBytes,
	}

	if err := validateListenAddr(cfg.ListenAddr); err != nil {
		return Config{}, err
	}

	if cfg.DBPath = strings.TrimSpace(getenv(EnvDBPath)); cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(cfg.DataDir, "gateway.db")
	}

	masterRaw := getenv(EnvMasterKey)
	if masterRaw == "" {
		return Config{}, &ValidationError{
			Variable: EnvMasterKey,
			Reason:   "必须设置 base64 编码的 32 字节密钥",
		}
	}
	masterKey, err := decodeMasterKey(masterRaw)
	if err != nil {
		return Config{}, &ValidationError{
			Variable: EnvMasterKey,
			Reason:   "必须为 base64 编码的 32 字节密钥",
		}
	}
	cfg.MasterKey = masterKey

	clientKey := getenv(EnvClientKey)
	if clientKey == "" {
		return Config{}, &ValidationError{
			Variable: EnvClientKey,
			Reason:   "必须设置，且至少 24 个字符",
		}
	}
	if utf8.RuneCountInString(clientKey) < 24 {
		return Config{}, &ValidationError{
			Variable: EnvClientKey,
			Reason:   "至少 24 个字符",
		}
	}
	cfg.ClientKey = clientKey

	// 管理员密码只在数据库尚无管理员记录时用于首次创建；长度策略由
	// adminauth 在创建时执行，配置层只负责承载与脱敏。
	cfg.AdminPassword = getenv(EnvAdminPassword)

	if cfg.CookieSecure, err = boolFromEnv(getenv, EnvCookieSecure, false); err != nil {
		return Config{}, err
	}

	if cfg.CodexContextWindow, err = intFromEnv(getenv, EnvCodexContextWindow, DefaultCodexContextWindow, 1, 0); err != nil {
		return Config{}, err
	}
	if cfg.UpstreamConnectTimeout, err = durationFromEnv(getenv, EnvUpstreamConnectTimeout, DefaultUpstreamConnectTimeout); err != nil {
		return Config{}, err
	}
	if cfg.UpstreamTLSHandshakeTimeout, err = durationFromEnv(getenv, EnvUpstreamTLSHandshakeTimeout, DefaultUpstreamTLSHandshakeTimeout); err != nil {
		return Config{}, err
	}
	if cfg.UpstreamResponseHeaderTimeout, err = durationFromEnv(getenv, EnvUpstreamHeaderTimeout, DefaultUpstreamResponseHeaderTimeout); err != nil {
		return Config{}, err
	}
	if cfg.UpstreamStreamIdleTimeout, err = durationFromEnv(getenv, EnvUpstreamStreamIdleTimeout, DefaultUpstreamStreamIdleTimeout); err != nil {
		return Config{}, err
	}
	if cfg.UpstreamRequestTimeout, err = durationFromEnv(getenv, EnvUpstreamRequestTimeout, DefaultUpstreamRequestTimeout); err != nil {
		return Config{}, err
	}
	if cfg.UpstreamStreamTimeout, err = durationFromEnv(getenv, EnvUpstreamStreamTimeout, DefaultUpstreamStreamTimeout); err != nil {
		return Config{}, err
	}
	if cfg.MaxAttempts, err = intFromEnv(getenv, EnvMaxAttempts, DefaultMaxAttempts, 1, DefaultMaxAttempts); err != nil {
		return Config{}, err
	}
	if cfg.CapacityWait, err = durationFromEnvAllowZero(getenv, EnvCapacityWait, DefaultCapacityWait); err != nil {
		return Config{}, err
	}
	if cfg.CaptureMaxBytes, err = int64FromEnv(getenv, EnvCaptureMaxBytes, DefaultCaptureMaxBytes); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// String 返回可安全写入日志的配置摘要，所有秘密字段固定输出 <redacted>。
func (c Config) String() string {
	return fmt.Sprintf(
		"Config{listen_addr=%s data_dir=%s db_path=%s master_key=%s client_key=%s admin_password=%s cookie_secure=%t codex_context_window=%d upstream_connect_timeout=%s upstream_tls_timeout=%s upstream_header_timeout=%s upstream_stream_idle_timeout=%s upstream_request_timeout=%s upstream_stream_timeout=%s max_attempts=%d capacity_wait=%s capture_max_bytes=%d}",
		c.ListenAddr,
		c.DataDir,
		c.DBPath,
		redacted,
		redacted,
		redacted,
		c.CookieSecure,
		c.CodexContextWindow,
		c.UpstreamConnectTimeout,
		c.UpstreamTLSHandshakeTimeout,
		c.UpstreamResponseHeaderTimeout,
		c.UpstreamStreamIdleTimeout,
		c.UpstreamRequestTimeout,
		c.UpstreamStreamTimeout,
		c.MaxAttempts,
		c.CapacityWait,
		c.CaptureMaxBytes,
	)
}

// GoString 让 %#v 也走脱敏输出，避免调试日志泄漏秘密字段。
func (c Config) GoString() string {
	return c.String()
}

// LogValue 让 slog.Any("config", cfg) 输出脱敏字符串。
func (c Config) LogValue() slog.Value {
	return slog.StringValue(c.String())
}

func envOrDefault(getenv func(string) string, name, def string) string {
	if value := getenv(name); value != "" {
		return value
	}
	return def
}

func decodeMasterKey(raw string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(raw)
	if err == nil && len(key) == 32 {
		return key, nil
	}
	key, err = base64.StdEncoding.WithPadding(base64.NoPadding).DecodeString(raw)
	if err == nil && len(key) == 32 {
		return key, nil
	}
	return nil, errors.New("master key must be base64 of exactly 32 bytes")
}

func validateListenAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return &ValidationError{
			Variable: EnvListenAddr,
			Reason:   "必须为 host:port 形式，例如 127.0.0.1:8080",
		}
	}
	if strings.ContainsAny(host, " \t\r\n/\\") {
		return &ValidationError{
			Variable: EnvListenAddr,
			Reason:   "主机部分包含非法字符",
		}
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return &ValidationError{
			Variable: EnvListenAddr,
			Reason:   "端口必须为 1-65535 的整数",
		}
	}
	return nil
}

func boolFromEnv(getenv func(string) string, name string, def bool) (bool, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, &ValidationError{
			Variable: name,
			Reason:   "必须为布尔值（true/false/1/0）",
		}
	}
	return value, nil
}

func intFromEnv(getenv func(string) string, name string, def, min, max int) (int, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < min || (max > 0 && value > max) {
		reason := fmt.Sprintf("必须为不小于 %d 的整数", min)
		if max > 0 {
			reason = fmt.Sprintf("必须为 %d-%d 的整数", min, max)
		}
		return 0, &ValidationError{Variable: name, Reason: reason}
	}
	return value, nil
}

func int64FromEnv(getenv func(string) string, name string, def int64) (int64, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, &ValidationError{
			Variable: name,
			Reason:   "必须为大于 0 的整数字节数",
		}
	}
	return value, nil
}

func durationFromEnv(getenv func(string) string, name string, def time.Duration) (time.Duration, error) {
	value, err := durationFromEnvAllowZero(getenv, name, def)
	if err != nil {
		return 0, err
	}
	if value <= 0 {
		return 0, &ValidationError{
			Variable: name,
			Reason:   "必须为大于 0 的时长（如 30s、5m）",
		}
	}
	return value, nil
}

func durationFromEnvAllowZero(getenv func(string) string, name string, def time.Duration) (time.Duration, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value < 0 {
		return 0, &ValidationError{
			Variable: name,
			Reason:   "必须为非负的时长（如 30s、5m）",
		}
	}
	return value, nil
}
