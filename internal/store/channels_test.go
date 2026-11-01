package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"ai-router/internal/secret"
)

// testChannelEnvelope 构造固定主密钥的 AES-GCM 信封。
func testChannelEnvelope(t *testing.T, fill byte) *secret.Envelope {
	t.Helper()
	key := make([]byte, secret.MasterKeySize)
	for i := range key {
		key[i] = fill
	}
	envelope, err := secret.NewEnvelope(key)
	if err != nil {
		t.Fatalf("secret.NewEnvelope: %v", err)
	}
	return envelope
}

// newChannelTestStore 返回迁移完成的临时 Store。
func newChannelTestStore(t *testing.T) *Store {
	t.Helper()
	s, _ := openMigratedStore(t, newTestPaths(t))
	return s
}

// newEncryptedChannelInput 构造带真实 AES-GCM 密文的渠道输入。
func newEncryptedChannelInput(
	t *testing.T,
	envelope *secret.Envelope,
	name, baseURL, protocol, plaintextKey string,
	priority, maxConcurrency int,
	enabled bool,
) ChannelInput {
	t.Helper()
	ciphertext, err := envelope.EncryptString(plaintextKey, secret.PurposeChannelKey)
	if err != nil {
		t.Fatalf("EncryptString: %v", err)
	}
	return ChannelInput{
		ChannelConfig: ChannelConfig{
			Name:           name,
			BaseURL:        baseURL,
			Protocol:       protocol,
			Priority:       priority,
			MaxConcurrency: maxConcurrency,
			Enabled:        enabled,
		},
		KeyCiphertext: ciphertext,
		KeyMask:       secret.MaskKey(plaintextKey),
	}
}

func rawChannelKey(t *testing.T, db *sql.DB, id int64) (ciphertext, mask string) {
	t.Helper()
	if err := db.QueryRow(
		`SELECT key_ciphertext, key_mask FROM channels WHERE id = ?`, id,
	).Scan(&ciphertext, &mask); err != nil {
		t.Fatalf("读取渠道密文: %v", err)
	}
	return ciphertext, mask
}

func channelRowCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM channels`).Scan(&count); err != nil {
		t.Fatalf("统计 channels: %v", err)
	}
	return count
}

// TestChannelsCRUDEncryptsKey 覆盖渠道 CRUD、密文落库、Key 轮换与运行态字段。
func TestChannelsCRUDEncryptsKey(t *testing.T) {
	ctx := context.Background()
	s := newChannelTestStore(t)
	repo := s.Channels()
	envelope := testChannelEnvelope(t, 0x11)

	const plaintextKey = "sk-live-0123456789abcdef"
	input := newEncryptedChannelInput(t,
		envelope,
		"  primary  ",
		"https://upstream.example.com/v1",
		ChannelProtocolOpenAIResponses,
		plaintextKey,
		10,
		3,
		true,
	)

	created, err := repo.Create(ctx, input)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID <= 0 {
		t.Fatalf("Create 返回的 ID = %d", created.ID)
	}
	if created.Name != "primary" {
		t.Errorf("Create 后 name = %q，期望去除首尾空白", created.Name)
	}
	if created.Protocol != ChannelProtocolOpenAIResponses || created.Priority != 10 ||
		created.MaxConcurrency != 3 || !created.Enabled {
		t.Errorf("Create 后配置不符：%+v", created)
	}

	ciphertext, mask := rawChannelKey(t, s.DB(), created.ID)
	if ciphertext == plaintextKey {
		t.Fatal("key_ciphertext 保存了明文")
	}
	if !strings.HasPrefix(ciphertext, secret.EnvelopeVersion+":") {
		t.Errorf("key_ciphertext 不是 v1 信封：%q", ciphertext)
	}
	if strings.Contains(ciphertext, plaintextKey) {
		t.Fatal("key_ciphertext 包含明文片段")
	}
	decrypted, err := envelope.DecryptString(ciphertext, secret.PurposeChannelKey)
	if err != nil {
		t.Fatalf("解密落库密文: %v", err)
	}
	if decrypted != plaintextKey {
		t.Fatalf("解密结果 = %q，期望原始 Key", decrypted)
	}
	if mask != secret.MaskKey(plaintextKey) {
		t.Errorf("key_mask = %q，期望 %q", mask, secret.MaskKey(plaintextKey))
	}

	// 明文不可通过 SQL 检索。
	var plaintextHits int
	if err := s.DB().QueryRow(
		`SELECT count(*) FROM channels WHERE key_ciphertext = ? OR key_mask = ?`,
		plaintextKey, plaintextKey,
	).Scan(&plaintextHits); err != nil {
		t.Fatalf("检索明文: %v", err)
	}
	if plaintextHits != 0 {
		t.Fatalf("数据库可用明文检索到 %d 行", plaintextHits)
	}

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("List = %+v，期望仅包含新建渠道", list)
	}

	// 更新 Key：旧密文被替换，新密文可解密。
	const rotatedKey = "sk-rotated-abcdefghijklmn"
	rotatedCiphertext, err := envelope.EncryptString(rotatedKey, secret.PurposeChannelKey)
	if err != nil {
		t.Fatalf("加密轮换 Key: %v", err)
	}
	rotatedMask := secret.MaskKey(rotatedKey)
	updated, err := repo.Update(ctx, created.ID, ChannelUpdate{
		ChannelConfig: ChannelConfig{
			Name:           "primary",
			BaseURL:        "https://upstream.example.com/v1",
			Protocol:       ChannelProtocolOpenAIResponses,
			Priority:       20,
			MaxConcurrency: 4,
			Enabled:        true,
		},
		KeyCiphertext: &rotatedCiphertext,
		KeyMask:       &rotatedMask,
	})
	if err != nil {
		t.Fatalf("Update 轮换 Key: %v", err)
	}
	if updated.Priority != 20 || updated.MaxConcurrency != 4 {
		t.Errorf("Update 后配置不符：%+v", updated)
	}
	newCiphertext, newMask := rawChannelKey(t, s.DB(), created.ID)
	if newCiphertext == ciphertext {
		t.Fatal("更新 Key 后 key_ciphertext 未变化")
	}
	newDecrypted, err := envelope.DecryptString(newCiphertext, secret.PurposeChannelKey)
	if err != nil {
		t.Fatalf("解密新密文: %v", err)
	}
	if newDecrypted != rotatedKey {
		t.Fatalf("新密文解密 = %q，期望轮换后的 Key", newDecrypted)
	}
	if newMask != rotatedMask {
		t.Errorf("更新后 key_mask = %q，期望 %q", newMask, rotatedMask)
	}
	var oldPlaintextHits int
	if err := s.DB().QueryRow(
		`SELECT count(*) FROM channels WHERE key_ciphertext = ? OR key_mask = ?`,
		plaintextKey, plaintextKey,
	).Scan(&oldPlaintextHits); err != nil {
		t.Fatalf("检索旧明文: %v", err)
	}
	if oldPlaintextHits != 0 {
		t.Fatalf("更新 Key 后仍可检索到旧明文")
	}

	// 不提供 Key：密文保持不变。
	if _, err := repo.Update(ctx, created.ID, ChannelUpdate{
		ChannelConfig: ChannelConfig{
			Name:           "primary-renamed",
			BaseURL:        "https://upstream.example.com/v1",
			Protocol:       ChannelProtocolOpenAIResponses,
			Priority:       20,
			MaxConcurrency: 4,
			Enabled:        true,
		},
	}); err != nil {
		t.Fatalf("Update 不带 Key: %v", err)
	}
	keptCiphertext, keptMask := rawChannelKey(t, s.DB(), created.ID)
	if keptCiphertext != newCiphertext || keptMask != newMask {
		t.Fatal("未提供 Key 时密文/脱敏值不应变化")
	}
	renamed, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if renamed.Name != "primary-renamed" {
		t.Errorf("更新后 name = %q", renamed.Name)
	}

	// 启停切换。
	disabled, err := repo.SetEnabled(ctx, created.ID, false)
	if err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if disabled.Enabled {
		t.Error("SetEnabled(false) 后 enabled 仍为 true")
	}

	// TouchLastUsed 更新 last_used_at。
	touchedAt := time.Now().UTC().Truncate(time.Second)
	if err := repo.TouchLastUsed(ctx, created.ID, touchedAt); err != nil {
		t.Fatalf("TouchLastUsed: %v", err)
	}
	afterTouch, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after TouchLastUsed: %v", err)
	}
	if afterTouch.LastUsedAt == nil || !afterTouch.LastUsedAt.Equal(touchedAt) {
		t.Fatalf("last_used_at = %v，期望 %v", afterTouch.LastUsedAt, touchedAt)
	}

	// 健康状态写入。
	latency := 87
	healthAt := time.Now().UTC().Truncate(time.Second)
	if err := repo.UpdateHealth(ctx, created.ID, ChannelHealth{
		ConnectionRevision: afterTouch.ConnectionRevision,
		Status:             "healthy",
		LatencyMS:          &latency,
		At:                 healthAt,
		Error:              "",
	}); err != nil {
		t.Fatalf("UpdateHealth: %v", err)
	}
	afterHealth, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after UpdateHealth: %v", err)
	}
	if afterHealth.LastHealthStatus != "healthy" || afterHealth.LastHealthLatencyMS == nil ||
		*afterHealth.LastHealthLatencyMS != latency {
		t.Errorf("健康状态未写入：%+v", afterHealth)
	}
	if afterHealth.LastHealthAt == nil || !afterHealth.LastHealthAt.Equal(healthAt) {
		t.Errorf("last_health_at = %v，期望 %v", afterHealth.LastHealthAt, healthAt)
	}

	// 删除后读取返回 ErrChannelNotFound。
	if err := repo.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.Get(ctx, created.ID); !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("删除后 Get 错误 = %v，期望 ErrChannelNotFound", err)
	}
	if err := repo.Delete(ctx, created.ID); !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("重复删除错误 = %v，期望 ErrChannelNotFound", err)
	}
	if count := channelRowCount(t, s.DB()); count != 0 {
		t.Fatalf("删除后 channels 行数 = %d", count)
	}
}

// TestChannelsValidation 覆盖 base URL、protocol、priority、并发上限与 name
// 的校验；失败输入不得落库。
func TestChannelsValidation(t *testing.T) {
	ctx := context.Background()
	s := newChannelTestStore(t)
	repo := s.Channels()
	envelope := testChannelEnvelope(t, 0x22)

	valid := func() ChannelInput {
		return newEncryptedChannelInput(t,
			envelope, "valid", "https://upstream.example.com/v1",
			ChannelProtocolOpenAIResponses, "sk-valid-0123456789", 0, 1, true)
	}

	tests := []struct {
		name   string
		mutate func(*ChannelInput)
	}{
		{"空 name", func(in *ChannelInput) { in.Name = "   " }},
		{"name 含控制字符", func(in *ChannelInput) { in.Name = "bad\nname" }},
		{"非 http/https", func(in *ChannelInput) { in.BaseURL = "ftp://upstream.example.com/v1" }},
		{"缺 scheme", func(in *ChannelInput) { in.BaseURL = "upstream.example.com/v1" }},
		{"缺 host", func(in *ChannelInput) { in.BaseURL = "https:///v1" }},
		{"userinfo", func(in *ChannelInput) { in.BaseURL = "https://user:pass@upstream.example.com/v1" }},
		{"fragment", func(in *ChannelInput) { in.BaseURL = "https://upstream.example.com/v1#frag" }},
		{"未知协议", func(in *ChannelInput) { in.Protocol = "openai" }},
		{"priority 过小", func(in *ChannelInput) { in.Priority = -1 }},
		{"priority 过大", func(in *ChannelInput) { in.Priority = 1001 }},
		{"并发为 0", func(in *ChannelInput) { in.MaxConcurrency = 0 }},
		{"并发超过 64", func(in *ChannelInput) { in.MaxConcurrency = 65 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := valid()
			tt.mutate(&input)
			if _, err := repo.Create(ctx, input); !errors.Is(err, ErrInvalidChannel) {
				t.Fatalf("Create 错误 = %v，期望 ErrInvalidChannel", err)
			}
		})
	}
	if count := channelRowCount(t, s.DB()); count != 0 {
		t.Fatalf("非法输入落库 %d 行", count)
	}

	// 校验错误只描述规则，不回显输入内容。
	bad := valid()
	bad.BaseURL = "https://user:secret@upstream.example.com/v1?token=abc"
	_, err := repo.Create(ctx, bad)
	if err == nil {
		t.Fatal("含 userinfo 的 base URL 应被拒绝")
	}
	for _, leaked := range []string{"secret", "token=abc", "user:"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("校验错误泄漏输入 %q：%v", leaked, err)
		}
	}
}

// TestChannelsNameConflict 覆盖创建/更新时的 UNIQUE 冲突及原记录保留。
func TestChannelsNameConflict(t *testing.T) {
	ctx := context.Background()
	s := newChannelTestStore(t)
	repo := s.Channels()
	envelope := testChannelEnvelope(t, 0x33)

	first, err := repo.Create(ctx, newEncryptedChannelInput(t,
		envelope, "shared-name", "https://a.example.com/v1",
		ChannelProtocolOpenAIResponses, "sk-first-0123456789", 0, 1, true))
	if err != nil {
		t.Fatalf("Create first: %v", err)
	}
	second, err := repo.Create(ctx, newEncryptedChannelInput(t,
		envelope, "second", "https://b.example.com/v1",
		ChannelProtocolOpenAIChat, "sk-second-0123456789", 1, 2, true))
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}

	if _, err := repo.Create(ctx, newEncryptedChannelInput(t,
		envelope, "shared-name", "https://c.example.com/v1",
		ChannelProtocolOpenAIResponses, "sk-third-0123456789", 2, 1, true)); !errors.Is(err, ErrChannelNameConflict) {
		t.Fatalf("重复 name 创建错误 = %v，期望 ErrChannelNameConflict", err)
	}
	if count := channelRowCount(t, s.DB()); count != 2 {
		t.Fatalf("重复创建后 channels 行数 = %d，期望 2", count)
	}

	// 更新为已有名称同样冲突，且目标记录保持原值。
	_, err = repo.Update(ctx, second.ID, ChannelUpdate{
		ChannelConfig: ChannelConfig{
			Name:           "shared-name",
			BaseURL:        second.BaseURL,
			Protocol:       second.Protocol,
			Priority:       second.Priority,
			MaxConcurrency: second.MaxConcurrency,
			Enabled:        second.Enabled,
		},
	})
	if !errors.Is(err, ErrChannelNameConflict) {
		t.Fatalf("重复 name 更新错误 = %v，期望 ErrChannelNameConflict", err)
	}
	reloaded, err := repo.Get(ctx, second.ID)
	if err != nil {
		t.Fatalf("Get second: %v", err)
	}
	if reloaded.Name != "second" {
		t.Fatalf("冲突更新后 second.name = %q，期望保持 second", reloaded.Name)
	}
	firstReloaded, err := repo.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("Get first: %v", err)
	}
	if firstReloaded.Name != "shared-name" {
		t.Fatalf("first.name = %q", firstReloaded.Name)
	}
}

// TestChannelsDeleteCascadesAndPreservesHistory 覆盖仓储层删除：路由级联
// 删除，日志/尝试的 channel_id 置 NULL 且 channel_name 快照保留。
func TestChannelsDeleteCascadesAndPreservesHistory(t *testing.T) {
	ctx := context.Background()
	s := newChannelTestStore(t)
	repo := s.Channels()
	envelope := testChannelEnvelope(t, 0x44)

	channel, err := repo.Create(ctx, newEncryptedChannelInput(t,
		envelope, "history-channel", "https://upstream.example.com/v1",
		ChannelProtocolOpenAIResponses, "sk-history-0123456789", 5, 2, true))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	mustExec(t, s.DB(), `
		INSERT INTO model_routes (client_model, channel_id, upstream_model, priority, enabled, created_at, updated_at)
		VALUES ('client-model', ?, 'upstream-model', 5, 1, ?, ?)`,
		channel.ID, now, now,
	)
	mustExec(t, s.DB(), `
		INSERT INTO request_logs (
			request_id, protocol, endpoint, model, channel_id, channel_name, status,
			http_status, stream, attempt_count, retry_count, latency_ms, semantic_committed, started_at
		) VALUES ('req-history', 'openai_responses', '/v1/responses', 'client-model', ?, 'history-channel', 'completed',
			200, 1, 1, 0, 120, 0, ?)`,
		channel.ID, now,
	)
	mustExec(t, s.DB(), `
		INSERT INTO request_attempts (
			request_id, attempt_index, channel_id, channel_name, upstream_model, status,
			http_status, latency_ms, started_at
		) VALUES ('req-history', 1, ?, 'history-channel', 'upstream-model', 'success', 200, 110, ?)`,
		channel.ID, now,
	)

	if err := repo.Delete(ctx, channel.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	var routeCount int
	if err := s.DB().QueryRow(`SELECT count(*) FROM model_routes`).Scan(&routeCount); err != nil {
		t.Fatalf("统计 model_routes: %v", err)
	}
	if routeCount != 0 {
		t.Fatalf("删除渠道后 model_routes 行数 = %d，期望 0", routeCount)
	}

	var logChannelID sql.NullInt64
	var logChannelName string
	if err := s.DB().QueryRow(
		`SELECT channel_id, channel_name FROM request_logs WHERE request_id = 'req-history'`,
	).Scan(&logChannelID, &logChannelName); err != nil {
		t.Fatalf("读取 request_logs: %v", err)
	}
	if logChannelID.Valid {
		t.Fatalf("request_logs.channel_id = %d，期望 NULL", logChannelID.Int64)
	}
	if logChannelName != "history-channel" {
		t.Fatalf("request_logs.channel_name = %q，期望保留快照", logChannelName)
	}

	var attemptChannelID sql.NullInt64
	var attemptChannelName string
	if err := s.DB().QueryRow(
		`SELECT channel_id, channel_name FROM request_attempts WHERE request_id = 'req-history'`,
	).Scan(&attemptChannelID, &attemptChannelName); err != nil {
		t.Fatalf("读取 request_attempts: %v", err)
	}
	if attemptChannelID.Valid {
		t.Fatalf("request_attempts.channel_id = %d，期望 NULL", attemptChannelID.Int64)
	}
	if attemptChannelName != "history-channel" {
		t.Fatalf("request_attempts.channel_name = %q，期望保留快照", attemptChannelName)
	}
}

func TestChannelConnectionRevisionAndHealth(t *testing.T) {
	ctx := context.Background()
	s := newChannelTestStore(t)
	repo := s.Channels()
	input := newEncryptedChannelInput(t, testChannelEnvelope(t, 0x31), "revision",
		"https://upstream.example/v1", ChannelProtocolOpenAIResponses, "sk-revision-test", 0, 1, true)
	channel, err := repo.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if channel.ConnectionRevision != 1 {
		t.Fatalf("initial revision = %d, want 1", channel.ConnectionRevision)
	}
	latency, duration := 12, 34
	health := ChannelHealth{Status: "healthy", LatencyMS: &latency, DurationMS: &duration,
		At: time.Now().UTC(), ConnectionRevision: 1, Model: "test-model",
		Protocol: ChannelProtocolOpenAIResponses, Kind: "semantic"}
	if err := repo.UpdateHealth(ctx, channel.ID, health); err != nil {
		t.Fatal(err)
	}
	config := input.ChannelConfig
	config.Name, config.Priority, config.MaxConcurrency, config.Enabled = "renamed", 3, 2, false
	channel, err = repo.Update(ctx, channel.ID, ChannelUpdate{ChannelConfig: config})
	if err != nil {
		t.Fatal(err)
	}
	if channel.ConnectionRevision != 1 || channel.LastHealthRevision != 1 ||
		channel.LastHealthModel != health.Model || channel.LastHealthProtocol != health.Protocol ||
		channel.LastHealthKind != health.Kind || channel.LastHealthDurationMS == nil ||
		*channel.LastHealthDurationMS != duration || channel.LastHealthStatus != "healthy" {
		t.Fatalf("non-connection update changed health metadata: %+v", channel)
	}
	for _, change := range []string{"url", "protocol", "key"} {
		t.Run(change, func(t *testing.T) {
			oldRevision := channel.ConnectionRevision
			update := ChannelUpdate{ChannelConfig: config}
			switch change {
			case "url":
				update.BaseURL = "https://changed.example/v1"
			case "protocol":
				update.Protocol = ChannelProtocolOpenAIChat
			case "key":
				// Explicitly replacing even an identical ciphertext invalidates the old probe.
				update.KeyCiphertext, update.KeyMask = &input.KeyCiphertext, &input.KeyMask
			}
			channel, err = repo.Update(ctx, channel.ID, update)
			if err != nil {
				t.Fatal(err)
			}
			config = update.ChannelConfig
			if channel.ConnectionRevision != oldRevision+1 || channel.LastHealthStatus != "" ||
				channel.LastHealthAt != nil || channel.LastHealthLatencyMS != nil || channel.LastError != "" ||
				channel.LastHealthRevision != 0 || channel.LastHealthModel != "" ||
				channel.LastHealthProtocol != "" || channel.LastHealthKind != "" || channel.LastHealthDurationMS != nil {
				t.Fatalf("connection update retained old health: %+v", channel)
			}
			health.ConnectionRevision = channel.ConnectionRevision
			if err := repo.UpdateHealth(ctx, channel.ID, health); err != nil {
				t.Fatal(err)
			}
			for _, staleRevision := range []int64{oldRevision, 0} {
				stale := health
				stale.ConnectionRevision, stale.Status, stale.Error = staleRevision, "unhealthy", "stale"
				if err := repo.UpdateHealth(ctx, channel.ID, stale); err != nil {
					t.Fatalf("stale health should be ignored: %v", err)
				}
			}
			current, err := repo.Get(ctx, channel.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.LastHealthStatus != "healthy" || current.LastError != "" || current.LastHealthRevision != channel.ConnectionRevision {
				t.Fatalf("stale probe overwrote current health: %+v", current)
			}
		})
	}
}

func TestValidateChannelKeysReadOnly(t *testing.T) {
	ctx := context.Background()
	s := newChannelTestStore(t)
	envelope := testChannelEnvelope(t, 0x42)
	decrypt := func(value string) (string, error) {
		return envelope.DecryptString(value, secret.PurposeChannelKey)
	}
	if err := s.ValidateChannelKeys(ctx, decrypt); err != nil {
		t.Fatalf("empty store: %v", err)
	}
	input := newEncryptedChannelInput(t, envelope, "disabled", "https://upstream.example/v1",
		ChannelProtocolOpenAIResponses, "sk-key-validation-sentinel", 0, 1, false)
	channel, err := s.Channels().Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateChannelKeys(ctx, decrypt); err != nil {
		t.Fatalf("valid disabled channel: %v", err)
	}
	wrongEnvelope := testChannelEnvelope(t, 0x43)
	for _, decode := range []func(string) (string, error){
		func(value string) (string, error) {
			return wrongEnvelope.DecryptString(value, secret.PurposeChannelKey)
		},
		func(value string) (string, error) {
			return "sk-key-validation-sentinel", errors.New(value + " secret-error-sentinel")
		},
		func(string) (string, error) { return "  ", nil },
	} {
		err := s.ValidateChannelKeys(ctx, decode)
		if err == nil || err.Error() != "store: 渠道密钥校验失败" {
			t.Fatalf("expected fixed safe validation error, got %v", err)
		}
	}
	ciphertext, mask := rawChannelKey(t, s.DB(), channel.ID)
	if ciphertext != input.KeyCiphertext || mask != input.KeyMask {
		t.Fatal("key validation changed persisted values")
	}
	mustExec(t, s.DB(), `UPDATE channels SET key_ciphertext = 'corrupt-ciphertext-sentinel' WHERE id = ?`, channel.ID)
	if err := s.ValidateChannelKeys(ctx, decrypt); err == nil || err.Error() != "store: 渠道密钥校验失败" {
		t.Fatalf("corrupt ciphertext error = %v", err)
	}
}

// TestChannelsTouchLastUsedMissing 覆盖对不存在渠道的运行态写入。
func TestChannelsTouchLastUsedMissing(t *testing.T) {
	ctx := context.Background()
	s := newChannelTestStore(t)
	repo := s.Channels()

	if err := repo.TouchLastUsed(ctx, 404, time.Now()); !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("TouchLastUsed 错误 = %v，期望 ErrChannelNotFound", err)
	}
	if err := repo.UpdateHealth(ctx, 404, ChannelHealth{Status: "unhealthy", At: time.Now(), ConnectionRevision: 1}); !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("UpdateHealth 错误 = %v，期望 ErrChannelNotFound", err)
	}
	if _, err := repo.SetEnabled(ctx, 404, false); !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("SetEnabled 错误 = %v，期望 ErrChannelNotFound", err)
	}
}
