package api

// 供外部脚本（油猴等）调用的离线下载开放端点。
//
// 与 /api/files/offline-download/* 的区别：那边要求管理员 session，
// 这边只认 task 类型的 API Key，且**不接受 account_id 参数** ——
// 账号一律取自 Key 的备注（note），避免一把 Key 能操作全部网盘。
//
// 备注写法（二选一，JSON 优先）：
//
//	{"offline_account_ids":[2]}
//	account_id=2
//
// 端点清单：
//
//	GET  /api/open/offline-download/capabilities    绑定的账号 + 各自能力
//	POST /api/open/offline-download/urls            提交链接（默认走内置下载器）
//	GET  /api/open/offline-download/tasks           任务列表/进度
//	POST /api/open/offline-download/tasks/batch-delete

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"litepan/internal/domain"
	"litepan/internal/offlinedownload"
)

// scriptOfflineBinding 是一次已鉴权请求的上下文：哪把 Key、能操作哪些账号。
type scriptOfflineBinding struct {
	key      *domain.ApiKey
	accounts []int64
}

func (b *scriptOfflineBinding) allows(accountID int64) bool {
	for _, id := range b.accounts {
		if id == accountID {
			return true
		}
	}
	return false
}

// resolveScriptOfflineBinding 解析 Authorization: Bearer <task key> 并读出绑定账号。
func (h *Handler) resolveScriptOfflineBinding(r *http.Request) (*scriptOfflineBinding, error) {
	raw, err := scriptBearerToken(r.Header.Get("Authorization"))
	if err != nil {
		return nil, err
	}
	key, err := h.apiKeys.ValidateTask(r.Context(), raw)
	if err != nil {
		return nil, err
	}
	accounts, err := parseOfflineAccountIDs(key.Note)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, domain.Errorf(domain.CodePermissionDenied, "该 API Key 未绑定网盘账号，请在其备注里填 account_id=<账号ID>")
	}
	return &scriptOfflineBinding{key: key, accounts: accounts}, nil
}

func scriptBearerToken(header string) (string, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return "", domain.Errorf(domain.CodeAdminAuthRequired, "缺少 Authorization")
	}
	if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return "", domain.Errorf(domain.CodePermissionDenied, "Authorization 格式错误")
	}
	token := strings.TrimSpace(header[7:])
	if token == "" {
		return "", domain.Errorf(domain.CodePermissionDenied, "缺少 API Key")
	}
	return token, nil
}

// parseOfflineAccountIDs 从 Key 备注里读绑定账号：先当 JSON，再退化到 account_id=1,2 写法。
func parseOfflineAccountIDs(note string) ([]int64, error) {
	note = strings.TrimSpace(note)
	if note == "" {
		return nil, nil
	}
	if strings.HasPrefix(note, "{") {
		var payload struct {
			OfflineAccountIDs []int64 `json:"offline_account_ids"`
			AccountID         int64   `json:"account_id"`
		}
		if err := json.Unmarshal([]byte(note), &payload); err != nil {
			return nil, domain.Errorf(domain.CodeValidation, "API Key 备注不是合法 JSON：%v", err)
		}
		out := make([]int64, 0, len(payload.OfflineAccountIDs)+1)
		for _, id := range payload.OfflineAccountIDs {
			if id > 0 {
				out = append(out, id)
			}
		}
		if payload.AccountID > 0 {
			out = append(out, payload.AccountID)
		}
		return out, nil
	}
	ids := make([]int64, 0, 2)
	for _, field := range strings.FieldsFunc(note, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' }) {
		pair := strings.SplitN(field, "=", 2)
		if len(pair) != 2 || !strings.EqualFold(strings.TrimSpace(pair[0]), "account_id") {
			continue
		}
		id, err := strconv.ParseInt(strings.TrimSpace(pair[1]), 10, 64)
		if err != nil || id <= 0 {
			return nil, domain.Errorf(domain.CodeValidation, "API Key 备注里的 account_id 不合法")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// scriptOfflineAccountDTO 是给脚本看的账号信息（不含任何凭据）。
type scriptOfflineAccountDTO struct {
	AccountID  int64                        `json:"account_id"`
	Name       string                       `json:"name"`
	DriverType string                       `json:"driver_type"`
	Capability offlinedownload.Capabilities `json:"capabilities"`
}

func (h *Handler) scriptOfflineAccounts(ctx context.Context, b *scriptOfflineBinding) ([]scriptOfflineAccountDTO, error) {
	out := make([]scriptOfflineAccountDTO, 0, len(b.accounts))
	for _, id := range b.accounts {
		name, driverType, err := h.accountSvc.LookupUploadAccount(ctx, id)
		if err != nil {
			return nil, err
		}
		capability, err := h.offlineDownloads.Capabilities(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, scriptOfflineAccountDTO{
			AccountID:  id,
			Name:       name,
			DriverType: driverType,
			Capability: capability,
		})
	}
	return out, nil
}

func (h *Handler) scriptOfflineCapabilities(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.apiKeys != nil && h.offlineDownloads != nil) {
		return
	}
	binding, err := h.resolveScriptOfflineBinding(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	accounts, err := h.scriptOfflineAccounts(r.Context(), binding)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, Resp{
		Success: true,
		Message: "获取离线下载能力成功",
		Data: map[string]any{
			"key_name": binding.key.Name,
			"accounts": accounts,
		},
	})
}

type scriptOfflineAddURLsReq struct {
	AccountID         int64    `json:"account_id"`
	ProviderKind      string   `json:"provider_kind"`
	URLs              []string `json:"urls"`
	FileName          string   `json:"file_name"`
	TargetParentID    string   `json:"target_parent_id"`
	TargetDisplayPath string   `json:"target_display_path"`
}

func (h *Handler) scriptOfflineAddURLs(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.apiKeys != nil && h.offlineDownloads != nil) {
		return
	}
	binding, err := h.resolveScriptOfflineBinding(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req scriptOfflineAddURLsReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	accountID := req.AccountID
	if accountID == 0 {
		if len(binding.accounts) == 1 {
			accountID = binding.accounts[0]
		} else {
			writeErr(w, domain.Errorf(domain.CodeValidation, "该 Key 绑定了多个账号，必须显式传 account_id"))
			return
		}
	}
	if !binding.allows(accountID) {
		writeErr(w, domain.Errorf(domain.CodePermissionDenied, "该 API Key 无权操作账号 %d", accountID))
		return
	}
	providerKind := strings.TrimSpace(req.ProviderKind)
	if providerKind == "" {
		providerKind = offlinedownload.ProviderBuiltin
	}
	tasks, err := h.offlineDownloads.AddURLs(r.Context(), offlinedownload.AddURLParams{
		AccountID:         accountID,
		ProviderKind:      providerKind,
		URLs:              req.URLs,
		FileName:          req.FileName,
		TargetParentID:    req.TargetParentID,
		TargetDisplayPath: req.TargetDisplayPath,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, Resp{Success: true, Message: "离线下载任务已提交", Data: tasks})
}

func (h *Handler) scriptOfflineListTasks(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.apiKeys != nil && h.offlineDownloads != nil) {
		return
	}
	binding, err := h.resolveScriptOfflineBinding(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	refresh := !strings.EqualFold(r.URL.Query().Get("refresh"), "false")
	var accountID int64
	if raw := strings.TrimSpace(r.URL.Query().Get("account_id")); raw != "" {
		id, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || id <= 0 {
			writeErr(w, domain.Errorf(domain.CodeValidation, "非法 account_id"))
			return
		}
		accountID = id
	}
	out := make([]offlinedownload.Task, 0, 8)
	for _, id := range binding.accounts {
		if !binding.allows(id) || (accountID > 0 && id != accountID) {
			continue
		}
		tasks, err := h.offlineDownloads.List(r.Context(), id, refresh && accountID > 0)
		if err != nil {
			writeErr(w, err)
			return
		}
		out = append(out, tasks...)
	}
	writeJSON(w, http.StatusOK, Resp{Success: true, Message: "获取离线下载任务成功", Data: out})
}

func (h *Handler) scriptOfflineBatchDeleteTasks(w http.ResponseWriter, r *http.Request) {
	if !ensureServiceReady(w, h.apiKeys != nil && h.offlineDownloads != nil) {
		return
	}
	if _, err := h.resolveScriptOfflineBinding(r); err != nil {
		writeErr(w, err)
		return
	}
	var req batchDeleteOfflineTasksReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	result := h.offlineDownloads.BatchDelete(r.Context(), req.TaskIDs)
	writeJSON(w, http.StatusOK, Resp{Success: true, Message: "批量删除离线下载任务完成", Data: result})
}
