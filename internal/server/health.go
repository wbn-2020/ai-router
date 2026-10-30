package server

import (
	"context"
	"net/http"
)

const (
	healthzPath = "/healthz"
	readyzPath  = "/readyz"
)

// 就绪失败原因：稳定、可供运维/脚本识别的机器可读代码。
const (
	reasonShuttingDown        = "shutting_down"
	reasonMasterKeyNotLoaded  = "master_key_not_loaded"
	reasonDatabaseUnavailable = "database_unavailable"
	reasonSchemaCheckFailed   = "schema_check_failed"
	reasonMigrationPending    = "migration_pending"
	reasonSchemaAhead         = "schema_version_ahead"
)

type healthResponse struct {
	Status string `json:"status"`
}

type readinessResponse struct {
	Status                string `json:"status"`
	Reason                string `json:"reason,omitempty"`
	SchemaVersion         *int   `json:"schema_version,omitempty"`
	RequiredSchemaVersion int    `json:"required_schema_version,omitempty"`
}

// handleHealthz 是进程存活探针：只要 HTTP 服务还能响应就返回 200。
// 数据库故障属于就绪问题，由 /readyz 报告。
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if !methodAllowed(r, http.MethodGet, http.MethodHead) {
		writeMethodNotAllowed(w, r, "GET, HEAD")
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

// handleReadyz 检查退出状态、主密钥、数据库连通性和 schema 版本。
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if !methodAllowed(r, http.MethodGet, http.MethodHead) {
		writeMethodNotAllowed(w, r, "GET, HEAD")
		return
	}

	if s.draining.Load() {
		s.writeNotReady(w, reasonShuttingDown, nil)
		return
	}
	if !s.masterKeyLoaded {
		s.writeNotReady(w, reasonMasterKeyNotLoaded, nil)
		return
	}
	if s.store == nil {
		s.writeNotReady(w, reasonDatabaseUnavailable, nil)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), readinessCheckTimeout)
	defer cancel()

	if err := s.store.Ping(ctx); err != nil {
		s.writeNotReady(w, reasonDatabaseUnavailable, nil)
		return
	}

	version, err := s.store.SchemaVersion(ctx)
	if err != nil {
		s.writeNotReady(w, reasonSchemaCheckFailed, nil)
		return
	}

	switch {
	case version < s.requiredSchemaVersion:
		s.writeNotReady(w, reasonMigrationPending, &version)
	case version > s.requiredSchemaVersion:
		s.writeNotReady(w, reasonSchemaAhead, &version)
	default:
		writeJSON(w, http.StatusOK, readinessResponse{
			Status:                "ready",
			SchemaVersion:         &version,
			RequiredSchemaVersion: s.requiredSchemaVersion,
		})
	}
}

func (s *Server) writeNotReady(w http.ResponseWriter, reason string, version *int) {
	writeJSON(w, http.StatusServiceUnavailable, readinessResponse{
		Status:                "unavailable",
		Reason:                reason,
		SchemaVersion:         version,
		RequiredSchemaVersion: s.requiredSchemaVersion,
	})
}
