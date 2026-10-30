package config_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-router/internal/config"
)

func validEnv() map[string]string {
	return map[string]string{
		config.EnvMasterKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("M"), 32)),
		config.EnvClientKey: "client-key-0123456789abcdef",
	}
}

func loadFromMap(env map[string]string) (config.Config, error) {
	return config.LoadFrom(func(name string) string {
		return env[name]
	})
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := loadFromMap(validEnv())
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	if cfg.ListenAddr != "127.0.0.1:8080" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, "127.0.0.1:8080")
	}
	if cfg.DataDir != "data" {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, "data")
	}
	if want := filepath.Join("data", "gateway.db"); cfg.DBPath != want {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, want)
	}
	if len(cfg.MasterKey) != 32 {
		t.Errorf("len(MasterKey) = %d, want 32", len(cfg.MasterKey))
	}
	if cfg.ClientKey != validEnv()[config.EnvClientKey] {
		t.Errorf("ClientKey 未按预期载入")
	}
	if cfg.AdminPassword != "" {
		t.Errorf("AdminPassword = %q, want empty", cfg.AdminPassword)
	}
	if cfg.CookieSecure {
		t.Errorf("CookieSecure = true, want false")
	}
	if cfg.CodexContextWindow != 128000 {
		t.Errorf("CodexContextWindow = %d, want 128000", cfg.CodexContextWindow)
	}
	if cfg.UpstreamConnectTimeout != 10*time.Second {
		t.Errorf("UpstreamConnectTimeout = %s, want 10s", cfg.UpstreamConnectTimeout)
	}
	if cfg.UpstreamTLSHandshakeTimeout != 10*time.Second {
		t.Errorf("UpstreamTLSHandshakeTimeout = %s, want 10s", cfg.UpstreamTLSHandshakeTimeout)
	}
	if cfg.UpstreamResponseHeaderTimeout != 180*time.Second {
		t.Errorf("UpstreamResponseHeaderTimeout = %s, want 180s", cfg.UpstreamResponseHeaderTimeout)
	}
	if cfg.UpstreamStreamIdleTimeout != 300*time.Second {
		t.Errorf("UpstreamStreamIdleTimeout = %s, want 300s", cfg.UpstreamStreamIdleTimeout)
	}
	if cfg.UpstreamRequestTimeout != 300*time.Second {
		t.Errorf("UpstreamRequestTimeout = %s, want 300s", cfg.UpstreamRequestTimeout)
	}
	if cfg.UpstreamStreamTimeout != 15*time.Minute {
		t.Errorf("UpstreamStreamTimeout = %s, want 15m0s", cfg.UpstreamStreamTimeout)
	}
	if cfg.MaxAttempts != 3 {
		t.Errorf("MaxAttempts = %d, want 3", cfg.MaxAttempts)
	}
	if cfg.CapacityWait != 2*time.Second {
		t.Errorf("CapacityWait = %s, want 2s", cfg.CapacityWait)
	}
	if cfg.CaptureMaxBytes != 8<<20 {
		t.Errorf("CaptureMaxBytes = %d, want %d", cfg.CaptureMaxBytes, int64(8<<20))
	}
}

func TestDBPathDerivedFromDataDir(t *testing.T) {
	dataDir := t.TempDir()
	env := validEnv()
	env[config.EnvDataDir] = dataDir

	cfg, err := loadFromMap(env)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if want := filepath.Join(dataDir, "gateway.db"); cfg.DBPath != want {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "custom.db")
	env := validEnv()
	env[config.EnvListenAddr] = "0.0.0.0:9090"
	env[config.EnvDataDir] = dataDir
	env[config.EnvDBPath] = dbPath
	env[config.EnvAdminPassword] = "admin-password-1234"
	env[config.EnvCookieSecure] = "true"
	env[config.EnvCodexContextWindow] = "64000"
	env[config.EnvUpstreamConnectTimeout] = "3s"
	env[config.EnvUpstreamTLSHandshakeTimeout] = "4s"
	env[config.EnvUpstreamHeaderTimeout] = "5s"
	env[config.EnvUpstreamStreamIdleTimeout] = "6s"
	env[config.EnvUpstreamRequestTimeout] = "7s"
	env[config.EnvUpstreamStreamTimeout] = "8m"
	env[config.EnvMaxAttempts] = "2"
	env[config.EnvCapacityWait] = "1s"
	env[config.EnvCaptureMaxBytes] = "1048576"

	cfg, err := loadFromMap(env)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	if cfg.ListenAddr != "0.0.0.0:9090" {
		t.Errorf("ListenAddr = %q", cfg.ListenAddr)
	}
	if cfg.DataDir != dataDir {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, dataDir)
	}
	if cfg.DBPath != dbPath {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, dbPath)
	}
	if cfg.AdminPassword != "admin-password-1234" {
		t.Errorf("AdminPassword 未按预期载入")
	}
	if !cfg.CookieSecure {
		t.Errorf("CookieSecure = false, want true")
	}
	if cfg.CodexContextWindow != 64000 {
		t.Errorf("CodexContextWindow = %d, want 64000", cfg.CodexContextWindow)
	}
	if cfg.UpstreamConnectTimeout != 3*time.Second ||
		cfg.UpstreamTLSHandshakeTimeout != 4*time.Second ||
		cfg.UpstreamResponseHeaderTimeout != 5*time.Second ||
		cfg.UpstreamStreamIdleTimeout != 6*time.Second ||
		cfg.UpstreamRequestTimeout != 7*time.Second ||
		cfg.UpstreamStreamTimeout != 8*time.Minute {
		t.Errorf("上游超时覆盖未生效: %+v", cfg)
	}
	if cfg.MaxAttempts != 2 {
		t.Errorf("MaxAttempts = %d, want 2", cfg.MaxAttempts)
	}
	if cfg.CapacityWait != time.Second {
		t.Errorf("CapacityWait = %s, want 1s", cfg.CapacityWait)
	}
	if cfg.CaptureMaxBytes != 1048576 {
		t.Errorf("CaptureMaxBytes = %d, want 1048576", cfg.CaptureMaxBytes)
	}
}

func TestLoadValidationFailures(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(map[string]string)
		variable string
		secret   string // 错误信息中不得出现的原始值
	}{
		{
			name: "缺失主密钥",
			mutate: func(env map[string]string) {
				delete(env, config.EnvMasterKey)
			},
			variable: config.EnvMasterKey,
		},
		{
			name: "主密钥非 base64",
			mutate: func(env map[string]string) {
				env[config.EnvMasterKey] = "not-base64-!@#$%^&*()"
			},
			variable: config.EnvMasterKey,
			secret:   "not-base64-!@#$%^&*()",
		},
		{
			name: "主密钥 31 字节",
			mutate: func(env map[string]string) {
				env[config.EnvMasterKey] = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("X"), 31))
			},
			variable: config.EnvMasterKey,
		},
		{
			name: "主密钥 33 字节",
			mutate: func(env map[string]string) {
				env[config.EnvMasterKey] = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("Y"), 33))
			},
			variable: config.EnvMasterKey,
		},
		{
			name: "缺失客户端 Key",
			mutate: func(env map[string]string) {
				delete(env, config.EnvClientKey)
			},
			variable: config.EnvClientKey,
		},
		{
			name: "客户端 Key 短于 24 字符",
			mutate: func(env map[string]string) {
				env[config.EnvClientKey] = "short-client-key-value"
			},
			variable: config.EnvClientKey,
			secret:   "short-client-key-value",
		},
		{
			name: "监听地址缺端口",
			mutate: func(env map[string]string) {
				env[config.EnvListenAddr] = "127.0.0.1"
			},
			variable: config.EnvListenAddr,
		},
		{
			name: "监听地址端口非法",
			mutate: func(env map[string]string) {
				env[config.EnvListenAddr] = "127.0.0.1:70000"
			},
			variable: config.EnvListenAddr,
		},
		{
			name: "监听地址端口非数字",
			mutate: func(env map[string]string) {
				env[config.EnvListenAddr] = "127.0.0.1:http"
			},
			variable: config.EnvListenAddr,
		},
		{
			name: "监听地址带协议",
			mutate: func(env map[string]string) {
				env[config.EnvListenAddr] = "http://127.0.0.1:8080"
			},
			variable: config.EnvListenAddr,
		},
		{
			name: "CookieSecure 非布尔",
			mutate: func(env map[string]string) {
				env[config.EnvCookieSecure] = "maybe"
			},
			variable: config.EnvCookieSecure,
			secret:   "maybe",
		},
		{
			name: "上下文窗口为 0",
			mutate: func(env map[string]string) {
				env[config.EnvCodexContextWindow] = "0"
			},
			variable: config.EnvCodexContextWindow,
		},
		{
			name: "上游超时为负",
			mutate: func(env map[string]string) {
				env[config.EnvUpstreamRequestTimeout] = "-1s"
			},
			variable: config.EnvUpstreamRequestTimeout,
			secret:   "-1s",
		},
		{
			name: "上游超时格式非法",
			mutate: func(env map[string]string) {
				env[config.EnvUpstreamStreamTimeout] = "ten minutes"
			},
			variable: config.EnvUpstreamStreamTimeout,
			secret:   "ten minutes",
		},
		{
			name: "重试上限为 0",
			mutate: func(env map[string]string) {
				env[config.EnvMaxAttempts] = "0"
			},
			variable: config.EnvMaxAttempts,
		},
		{
			name: "重试上限超过 3",
			mutate: func(env map[string]string) {
				env[config.EnvMaxAttempts] = "4"
			},
			variable: config.EnvMaxAttempts,
		},
		{
			name: "容量等待为负",
			mutate: func(env map[string]string) {
				env[config.EnvCapacityWait] = "-2s"
			},
			variable: config.EnvCapacityWait,
			secret:   "-2s",
		},
		{
			name: "捕获上限非数字",
			mutate: func(env map[string]string) {
				env[config.EnvCaptureMaxBytes] = "8MB"
			},
			variable: config.EnvCaptureMaxBytes,
			secret:   "8MB",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			tt.mutate(env)
			cfg, err := loadFromMap(env)
			if err == nil {
				t.Fatalf("LoadFrom() 成功，want error")
			}

			var verr *config.ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("error 类型 = %T, want *config.ValidationError: %v", err, err)
			}
			if verr.Variable != tt.variable {
				t.Errorf("Variable = %q, want %q", verr.Variable, tt.variable)
			}
			if tt.secret != "" && strings.Contains(err.Error(), tt.secret) {
				t.Errorf("错误信息回显了配置值 %q: %v", tt.secret, err)
			}
			if cfg.ListenAddr != "" || cfg.MasterKey != nil {
				t.Errorf("失败时返回了非零 Config")
			}
		})
	}
}

func TestLoadAcceptsEmptyHostAndUnpaddedMasterKey(t *testing.T) {
	env := validEnv()
	env[config.EnvListenAddr] = ":9090"
	raw := bytes.Repeat([]byte("Z"), 32)
	env[config.EnvMasterKey] = base64.RawStdEncoding.EncodeToString(raw)

	cfg, err := loadFromMap(env)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if cfg.ListenAddr != ":9090" {
		t.Errorf("ListenAddr = %q, want :9090", cfg.ListenAddr)
	}
	if !bytes.Equal(cfg.MasterKey, raw) {
		t.Errorf("MasterKey 未正确解码")
	}
}

func TestConfigStringRedactsSecrets(t *testing.T) {
	masterRaw := bytes.Repeat([]byte("M"), 32)
	masterEncoded := base64.StdEncoding.EncodeToString(masterRaw)
	clientKey := "client-secret-ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	adminPassword := "admin-secret-do-not-log"
	env := map[string]string{
		config.EnvMasterKey:     masterEncoded,
		config.EnvClientKey:     clientKey,
		config.EnvAdminPassword: adminPassword,
	}

	cfg, err := loadFromMap(env)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	outputs := []string{
		cfg.String(),
		fmt.Sprintf("%v", cfg),
		fmt.Sprintf("%+v", cfg),
		fmt.Sprintf("%#v", cfg),
	}

	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuffer, nil))
	logger.Info("config loaded", "config", cfg)
	outputs = append(outputs, logBuffer.String())

	for _, out := range outputs {
		if strings.Contains(out, masterEncoded) {
			t.Errorf("输出包含 base64 主密钥: %s", out)
		}
		if strings.Contains(out, string(masterRaw)) {
			t.Errorf("输出包含原始主密钥: %s", out)
		}
		if strings.Contains(out, clientKey) {
			t.Errorf("输出包含客户端 Key: %s", out)
		}
		if strings.Contains(out, adminPassword) {
			t.Errorf("输出包含管理员密码: %s", out)
		}
		if !strings.Contains(out, "<redacted>") {
			t.Errorf("输出缺少脱敏占位符: %s", out)
		}
	}
}
