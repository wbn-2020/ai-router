// models.go 实现管理端“从上游获取模型列表”的唯一出站调用。
//
// 边界：
//   - 只支持已有原生 adapter 的 OpenAI Responses / Chat 协议；
//   - GET {base_url}/models，使用上游 Key 做 Bearer 认证；
//   - 响应体有上限，只解析 data[].id，不保存 body；
//   - 错误不包含 Key、Base URL 查询串或上游响应内容。
package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	// maxModelsResponseBytes 是模型列表响应的读取上限。
	maxModelsResponseBytes int64 = 4 << 20
	// modelDiscoveryTimeout 是模型发现请求的总时限；比数据面请求更短，
	// 避免管理端等待几分钟。
	modelDiscoveryTimeout = 15 * time.Second
)

// ErrModelsUnavailable 是模型发现失败的稳定哨兵。
var ErrModelsUnavailable = errors.New("upstream: 获取模型列表失败")

// ModelsHTTPError 表示上游对 /models 返回了非 2xx。
//
// 管理端可据此区分鉴权/限流，但对外只返回稳定错误码，不回显上游 body。
type ModelsHTTPError struct {
	StatusCode int
}

func (e *ModelsHTTPError) Error() string {
	return fmt.Sprintf("upstream: 获取模型列表返回 HTTP %d", e.StatusCode)
}

// Unwrap 让 errors.Is(err, ErrModelsUnavailable) 成立。
func (e *ModelsHTTPError) Unwrap() error {
	return ErrModelsUnavailable
}

// ListModels 请求上游 /models 并返回去重、排序后的模型 ID。
//
// Target 必须包含 BaseURL/Protocol/APIKey；Anthropic 仍为预留协议，返回
// ErrUnsupportedProtocol。调用方负责把错误映射为管理端安全文案。
func (c *Client) ListModels(ctx context.Context, target Target) ([]string, error) {
	if c == nil || c.httpClient == nil {
		return nil, errors.New("upstream: client 未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := adapterFor(target.Protocol); err != nil {
		return nil, err
	}
	key := strings.TrimSpace(target.APIKey)
	if key == "" {
		return nil, fmt.Errorf("%w: 上游 Key 为空", ErrInvalidRequest)
	}

	endpoint, err := buildEndpointURL(target.BaseURL, "/models")
	if err != nil {
		return nil, err
	}

	timeout := c.requestTimeout
	if timeout <= 0 || timeout > modelDiscoveryTimeout {
		timeout = modelDiscoveryTimeout
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: 无法构造模型列表请求", ErrInvalidRequest)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+key)

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, classifyDoError(requestCtx, err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		// 丢弃少量 body，避免连接无法复用；错误不携带内容。
		_, _ = io.CopyN(io.Discard, response.Body, 4<<10)
		return nil, &ModelsHTTPError{StatusCode: response.StatusCode}
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxModelsResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: 读取响应失败", ErrModelsUnavailable)
	}
	if int64(len(body)) > maxModelsResponseBytes {
		return nil, fmt.Errorf("%w: 响应超过 %d 字节", ErrModelsUnavailable, maxModelsResponseBytes)
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("%w: 响应不是合法的模型列表", ErrModelsUnavailable)
	}

	seen := make(map[string]struct{}, len(payload.Data))
	models := make([]string, 0, len(payload.Data))
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, id)
	}
	sort.Strings(models)
	return models, nil
}
