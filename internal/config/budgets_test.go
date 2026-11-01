package config_test

import (
	"errors"
	"testing"
	"time"

	"ai-router/internal/config"
)

func TestRequestBudgetsFollowAttemptDefaults(t *testing.T) {
	env := validEnv()
	env[config.EnvUpstreamRequestTimeout] = "7s"
	env[config.EnvUpstreamStreamTimeout] = "8m"
	cfg, err := loadFromMap(env)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RequestTimeout != 7*time.Second || cfg.StreamTimeout != 8*time.Minute {
		t.Fatalf("请求预算未继承单次上游预算：%+v", cfg)
	}
	if cfg.FirstContentTimeout != 180*time.Second || cfg.DownstreamWriteTimeout != 30*time.Second ||
		cfg.ProbeTimeout != 60*time.Second || cfg.ModelDiscoveryTimeout != 30*time.Second {
		t.Fatalf("默认子预算错误：%+v", cfg)
	}
}

func TestRequestBudgetsCanBeConfigured(t *testing.T) {
	env := validEnv()
	env[config.EnvRequestTimeout] = "20s"
	env[config.EnvStreamTimeout] = "2m"
	env[config.EnvFirstContentTimeout] = "4s"
	env[config.EnvDownstreamWriteTimeout] = "5s"
	env[config.EnvProbeTimeout] = "6s"
	env[config.EnvModelDiscoveryTimeout] = "7s"
	cfg, err := loadFromMap(env)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RequestTimeout != 20*time.Second || cfg.StreamTimeout != 2*time.Minute ||
		cfg.FirstContentTimeout != 4*time.Second || cfg.DownstreamWriteTimeout != 5*time.Second ||
		cfg.ProbeTimeout != 6*time.Second || cfg.ModelDiscoveryTimeout != 7*time.Second {
		t.Fatalf("自定义预算错误：%+v", cfg)
	}
}

func TestRequestBudgetsRejectInvalidDurations(t *testing.T) {
	for _, name := range []string{config.EnvRequestTimeout, config.EnvStreamTimeout,
		config.EnvFirstContentTimeout, config.EnvDownstreamWriteTimeout,
		config.EnvProbeTimeout, config.EnvModelDiscoveryTimeout} {
		for _, value := range []string{"0", "-1s", "invalid"} {
			t.Run(name+"/"+value, func(t *testing.T) {
				env := validEnv()
				env[name] = value
				_, err := loadFromMap(env)
				var validation *config.ValidationError
				if !errors.As(err, &validation) || validation.Variable != name {
					t.Fatalf("配置 %s 应被拒绝：%v", name, err)
				}
			})
		}
	}
}
