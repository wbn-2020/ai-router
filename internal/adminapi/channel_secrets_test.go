package adminapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"ai-router/internal/secret"
)

// T08 上游 Key 明文揭示接口测试。

func channelKeyPath(id int64) string {
	return "/admin/api/channels/" + intString(id) + "/key"
}

// tamperEnvelope 翻转密文末字节，保持 v1 信封结构但破坏 GCM 认证标签。
func tamperEnvelope(t *testing.T, envelope string) string {
	t.Helper()
	parts := strings.Split(envelope, ":")
	if len(parts) != 3 {
		t.Fatalf("密文不是 v1 信封：%q", envelope)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(raw) == 0 {
		t.Fatalf("解码密文失败：%v", err)
	}
	raw[len(raw)-1] ^= 0xff
	return parts[0] + ":" + parts[1] + ":" + base64.RawURLEncoding.EncodeToString(raw)
}

// TestChannelKeyReveal 覆盖揭示明文、mask 一致性、no-store、列表脱敏与日志无 Key。
func TestChannelKeyReveal(t *testing.T) {
	api, st, _, cookie, logs := newChannelsAPI(t)
	created := createChannel(t, api, cookie, validChannelBody("reveal"))

	rec := apiRequest(t, api, http.MethodGet, channelKeyPath(created.ID), nil, "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("揭示 Key = %d, want 200（body=%s）", rec.Code, rec.Body.String())
	}
	assertNoStore(t, rec)

	revealed := decodeBody[channelKeyResponse](t, rec)
	if revealed.Key != testChannelKey {
		t.Fatalf("揭示的 Key 与创建时不一致：%q", revealed.Key)
	}
	if revealed.Mask != secret.MaskKey(testChannelKey) {
		t.Errorf("揭示接口 mask = %q，期望 %q", revealed.Mask, secret.MaskKey(testChannelKey))
	}
	if revealed.Mask != created.KeyMask {
		t.Errorf("揭示接口 mask = %q，与列表 mask %q 不一致", revealed.Mask, created.KeyMask)
	}

	ciphertext, _ := rawChannelSecrets(t, st, created.ID)
	if strings.Contains(rec.Body.String(), ciphertext) {
		t.Fatalf("揭示响应包含 key_ciphertext：%s", rec.Body.String())
	}

	// 列表接口仍只返回 mask，不因揭示接口而出现明文。
	list := apiRequest(t, api, http.MethodGet, "/admin/api/channels", nil, "", cookie)
	if list.Code != http.StatusOK {
		t.Fatalf("渠道列表 = %d, want 200（body=%s）", list.Code, list.Body.String())
	}
	assertNoStore(t, list)
	assertNoChannelSecret(t, list.Body.String(), testChannelKey)
	listBody := decodeBody[channelListResponse](t, list)
	if len(listBody.Channels) != 1 || listBody.Channels[0].KeyMask != created.KeyMask {
		t.Fatalf("列表脱敏值不符：%+v", listBody.Channels)
	}

	logOutput := logs.String()
	for _, leaked := range []string{testChannelKey, ciphertext} {
		if strings.Contains(logOutput, leaked) {
			t.Fatalf("日志泄漏 Key 或密文 %q：%s", leaked, logOutput)
		}
	}
}

// TestChannelKeyRevealDecryptFailure 覆盖篡改与损坏密文：统一 500，
// 响应与日志均不含明文、密文或损坏后的密文。
func TestChannelKeyRevealDecryptFailure(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(*testing.T, string) string
	}{
		{
			name:    "篡改密文",
			corrupt: tamperEnvelope,
		},
		{
			name: "信封格式损坏",
			corrupt: func(*testing.T, string) string {
				return "v1:corrupted-envelope-marker"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api, st, _, cookie, logs := newChannelsAPI(t)
			created := createChannel(t, api, cookie, validChannelBody("decrypt-failure"))
			ciphertext, _ := rawChannelSecrets(t, st, created.ID)
			corrupted := tt.corrupt(t, ciphertext)
			if _, err := st.DB().Exec(
				`UPDATE channels SET key_ciphertext = ? WHERE id = ?`, corrupted, created.ID,
			); err != nil {
				t.Fatalf("写入损坏密文: %v", err)
			}

			rec := apiRequest(t, api, http.MethodGet, channelKeyPath(created.ID), nil, "", cookie)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("解密失败 = %d, want 500（body=%s）", rec.Code, rec.Body.String())
			}
			assertNoStore(t, rec)

			var payload errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("解析错误响应: %v", err)
			}
			if payload.Error != "internal_error" {
				t.Errorf("错误码 = %q, want internal_error", payload.Error)
			}

			for _, leaked := range []string{testChannelKey, ciphertext, corrupted} {
				if strings.Contains(rec.Body.String(), leaked) {
					t.Fatalf("错误响应泄漏 %q：%s", leaked, rec.Body.String())
				}
				if strings.Contains(logs.String(), leaked) {
					t.Fatalf("日志泄漏 %q：%s", leaked, logs.String())
				}
			}
		})
	}
}

// TestChannelKeyRevealRequiresSession 覆盖认证、存在性与非法 ID。
func TestChannelKeyRevealRequiresSession(t *testing.T) {
	api, _, _, cookie, _ := newChannelsAPI(t)
	created := createChannel(t, api, cookie, validChannelBody("reveal-auth"))

	unauthorized := apiRequest(t, api, http.MethodGet, channelKeyPath(created.ID), nil, "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("未登录揭示 Key = %d, want 401（body=%s）",
			unauthorized.Code, unauthorized.Body.String())
	}
	assertNoStore(t, unauthorized)
	if strings.Contains(unauthorized.Body.String(), testChannelKey) {
		t.Fatalf("未登录响应泄漏 Key：%s", unauthorized.Body.String())
	}

	// 认证先于存在性判断：无 session 时即使渠道不存在也返回 401。
	missingUnauthorized := apiRequest(t, api,
		http.MethodGet, channelKeyPath(99999), nil, "")
	if missingUnauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("未登录揭示不存在渠道 = %d, want 401", missingUnauthorized.Code)
	}

	missing := apiRequest(t, api, http.MethodGet, channelKeyPath(99999), nil, "", cookie)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("揭示不存在渠道 = %d, want 404（body=%s）", missing.Code, missing.Body.String())
	}
	assertNoStore(t, missing)

	invalid := apiRequest(t, api, http.MethodGet,
		"/admin/api/channels/not-a-number/key", nil, "", cookie)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("非法渠道 ID 揭示 = %d, want 400（body=%s）", invalid.Code, invalid.Body.String())
	}
}
