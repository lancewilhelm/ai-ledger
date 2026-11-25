package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lancewilhelm/ai-ledger/internal/pricing"
	"github.com/lancewilhelm/ai-ledger/internal/protocol"
	"github.com/lancewilhelm/ai-ledger/internal/redact"
	"github.com/lancewilhelm/ai-ledger/internal/store"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339),
	}
	if s.cfg.Version != "" {
		resp["version"] = s.cfg.Version
	}
	writeJSON(w, http.StatusOK, resp)
}

func parseQueryFilter(r *http.Request) store.QueryFilter {
	q := r.URL.Query()
	var f store.QueryFilter
	if v := strings.TrimSpace(q.Get("from")); v != "" {
		if t, ok := parseParamTime(v, false); ok {
			f.From = &t
		}
	}
	if v := strings.TrimSpace(q.Get("to")); v != "" {
		if t, ok := parseParamTime(v, true); ok {
			f.To = &t
		}
	}
	f.Device = strings.TrimSpace(q.Get("device"))
	f.Harness = strings.TrimSpace(q.Get("harness"))
	f.Model = strings.TrimSpace(q.Get("model"))
	f.Project = strings.TrimSpace(q.Get("project"))
	f.Query = strings.TrimSpace(q.Get("q"))
	return f
}

func parseParamTime(v string, endOfDay bool) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		if endOfDay {
			return t.UTC().Add(24*time.Hour - time.Nanosecond), true
		}
		return t.UTC(), true
	}
	return time.Time{}, false
}

func parseIntParam(r *http.Request, name string, def int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	f := parseQueryFilter(r)
	ov, err := s.store.GetOverview(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ov)
}

func (s *Server) handleTimeseries(w http.ResponseWriter, r *http.Request) {
	f := parseQueryFilter(r)
	bucket := r.URL.Query().Get("bucket")
	if bucket == "" {
		bucket = "day"
	}
	groupBy := r.URL.Query().Get("group_by")
	pts, err := s.store.GetTimeseries(r.Context(), f, bucket, groupBy)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bucket":   bucket,
		"group_by": groupBy,
		"points":   pts,
	})
}

func (s *Server) handleBreakdown(w http.ResponseWriter, r *http.Request) {
	f := parseQueryFilter(r)
	by := r.URL.Query().Get("by")
	if by == "" {
		by = "model"
	}
	rows, err := s.store.GetBreakdown(r.Context(), f, by)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"by":   by,
		"rows": rows,
	})
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	f := parseQueryFilter(r)
	params := store.SessionListParams{
		Filter: f,
		Sort:   r.URL.Query().Get("sort"),
		Order:  r.URL.Query().Get("order"),
		Limit:  parseIntParam(r, "limit", 50),
		Offset: parseIntParam(r, "offset", 0),
	}
	sessions, total, err := s.store.ListSessions(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessions": sessions,
		"total":    total,
		"limit":    params.Limit,
		"offset":   params.Offset,
	})
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	detail, err := s.store.GetSessionDetail(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleGetSessionRaw(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	detail, err := s.store.GetSessionDetail(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rf, err := s.store.GetRawFileByID(r.Context(), detail.Session.SourceFileID)
	if err != nil {
		writeError(w, http.StatusNotFound, "raw file not found")
		return
	}
	absPath := s.archive.AbsPath(rf.ArchivePath)
	f, err := os.Open(absPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "archived file missing on disk")
		return
	}
	defer f.Close()

	filename := filepath.Base(rf.RelPath)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	_, _ = io.Copy(w, f)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	f := parseQueryFilter(r)
	limit := parseIntParam(r, "limit", 50)
	offset := parseIntParam(r, "offset", 0)
	hits, err := s.store.SearchMessages(r.Context(), f, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"query": f.Query,
		"hits":  hits,
	})
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	devs, err := s.store.ListDevices(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devs})
}

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseIntParam(r, "limit", 100)
	offset := parseIntParam(r, "offset", 0)
	files, total, err := s.store.ListRawFiles(r.Context(), q.Get("device"), q.Get("source"), q.Get("status"), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"files":  files,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

func (s *Server) handleFilters(w http.ResponseWriter, r *http.Request) {
	opts, err := s.store.GetFilterOptions(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, opts)
}

// handleExportTraining streams normalized sessions as JSONL ready for SFT/local model guidance,
// with secret scrubbing enabled by default (?redact=false to disable).
func (s *Server) handleExportTraining(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	doRedact := q.Get("redact") != "false" && q.Get("redact") != "0"
	includeThinking := q.Get("thinking") != "false" && q.Get("thinking") != "0"
	includeTools := q.Get("tools") != "false" && q.Get("tools") != "0"

	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="ai-ledger-training.jsonl"`)

	enc := json.NewEncoder(w)

	writeSession := func(sessionID string) error {
		detail, err := s.store.GetSessionDetail(r.Context(), sessionID)
		if err != nil {
			return err
		}
		if len(detail.Messages) == 0 {
			return nil
		}
		msgs := make([]map[string]any, 0, len(detail.Messages))
		for _, m := range detail.Messages {
			content := m.ContentText
			thinking := m.ThinkingText
			if doRedact {
				content = redact.Text(content)
				thinking = redact.Text(thinking)
			}
			item := map[string]any{
				"role":    m.Role,
				"content": content,
			}
			if includeThinking && thinking != "" {
				item["thinking"] = thinking
			}
			if m.Model != "" {
				item["model"] = m.Model
			}
			msgs = append(msgs, item)
		}

		record := map[string]any{
			"session_id": detail.Session.ID,
			"native_id":  detail.Session.NativeID,
			"harness":    detail.Session.Harness,
			"model":      detail.Session.ModelPrimary,
			"project":    detail.Session.ProjectName,
			"device":     detail.Session.DeviceName,
			"started_at": detail.Session.StartedAt.Format(time.RFC3339),
			"redacted":   doRedact,
			"messages":   msgs,
		}
		if includeTools && len(detail.ToolCalls) > 0 {
			tcs := make([]map[string]any, 0, len(detail.ToolCalls))
			for _, tc := range detail.ToolCalls {
				inStr := tc.InputJSON
				outStr := tc.OutputText
				if doRedact {
					inStr = redact.Text(inStr)
					outStr = redact.Text(outStr)
				}
				tcs = append(tcs, map[string]any{
					"tool_name": tc.ToolName,
					"call_id":   tc.CallID,
					"input":     inStr,
					"output":    outStr,
					"is_error":  tc.IsError,
				})
			}
			record["tool_calls"] = tcs
		}
		return enc.Encode(record)
	}

	if singleID := strings.TrimSpace(q.Get("session_id")); singleID != "" {
		_ = writeSession(singleID)
		return
	}

	f := parseQueryFilter(r)
	offset := 0
	const pageSize = 100
	for {
		sessions, _, err := s.store.ListSessions(r.Context(), store.SessionListParams{
			Filter: f,
			Sort:   "started_at",
			Order:  "asc",
			Limit:  pageSize,
			Offset: offset,
		})
		if err != nil || len(sessions) == 0 {
			break
		}
		for _, sess := range sessions {
			if err := writeSession(sess.ID); err != nil {
				return
			}
		}
		offset += len(sessions)
		if len(sessions) < pageSize {
			break
		}
	}
}

// --- Admin Endpoints ---

func (s *Server) handleAdminReparse(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FileID string `json:"file_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	if body.FileID != "" {
		if err := s.ParseFileNow(r.Context(), body.FileID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"parsed": 1, "failed": 0})
		return
	}

	parsed, failed, err := s.ReparseAll(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"parsed": parsed, "failed": failed})
}

func (s *Server) handleAdminCreateDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		Hostname string `json:"hostname"`
		OS       string `json:"os"`
		Arch     string `json:"arch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	plain, hash, err := GenerateToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	dev, err := s.store.CreateDevice(r.Context(), strings.TrimSpace(body.Name), body.Hostname, body.OS, body.Arch, hash)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"device": dev,
		"token":  plain,
	})
}

func (s *Server) handleAdminRevokeDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.RevokeDevice(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "device not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// --- Pricing Endpoints ---

func roundRate(v float64) float64 {
	return math.Round(v*1e6) / 1e6
}

func ptrFloat(v float64) *float64 {
	r := roundRate(v)
	return &r
}

func (s *Server) handleGetPricing(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	observed, err := s.store.ListObservedModels(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	overrides, err := s.store.ListPricingOverrides(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	rawSources := s.pricing.Sources()
	sources := make([]protocol.PricingSourceInfo, 0, len(rawSources))
	for _, src := range rawSources {
		sources = append(sources, protocol.PricingSourceInfo{
			Kind:   src.Kind,
			Label:  src.Label,
			Detail: src.Detail,
			Models: src.Models,
		})
	}

	observedViews := make([]protocol.ModelRateView, 0, len(observed))
	for _, om := range observed {
		lk := s.pricing.LookupDetailed(om.Model)
		pm := lk.Rate.PerMillion()
		view := protocol.ModelRateView{
			Model:            om.Model,
			Harnesses:        om.Harnesses,
			APICallCount:     om.APICallCount,
			TotalTokens:      om.TotalTokens,
			EstimatedCostUSD: om.EstimatedCostUSD,
			MatchedKey:       lk.MatchedKey,
			MatchType:        lk.MatchType,
			Source:           lk.Source,
			SourceDetail:     lk.SourceDetail,
			IsOverride:       lk.Source == pricing.SourceOverride,
			InputPerM:        roundRate(pm.Input),
			CachedInputPerM:  roundRate(pm.CachedInput),
			CacheWrite5mPerM: roundRate(pm.CacheWrite5m),
			CacheWrite1hPerM: roundRate(pm.CacheWrite1h),
			OutputPerM:       roundRate(pm.Output),
			Notes:            lk.Notes,
			UpdatedAt:        lk.UpdatedAt,
		}
		if lk.BaseFound {
			bpm := lk.BaseRate.PerMillion()
			view.BaseSource = lk.BaseSource
			view.BaseDetail = lk.BaseDetail
			view.BaseInputPerM = ptrFloat(bpm.Input)
			view.BaseCachedInputPerM = ptrFloat(bpm.CachedInput)
			view.BaseCacheWrite5mPerM = ptrFloat(bpm.CacheWrite5m)
			view.BaseCacheWrite1hPerM = ptrFloat(bpm.CacheWrite1h)
			view.BaseOutputPerM = ptrFloat(bpm.Output)
		}
		observedViews = append(observedViews, view)
	}

	catEntries := s.pricing.Catalog()
	catalogViews := make([]protocol.ModelRateView, 0, len(catEntries))
	for _, ce := range catEntries {
		pm := ce.Rate.PerMillion()
		view := protocol.ModelRateView{
			Model:            ce.Model,
			MatchedKey:       ce.Model,
			MatchType:        "exact",
			Source:           ce.Source,
			SourceDetail:     ce.SourceDetail,
			IsOverride:       ce.IsOverride,
			InputPerM:        roundRate(pm.Input),
			CachedInputPerM:  roundRate(pm.CachedInput),
			CacheWrite5mPerM: roundRate(pm.CacheWrite5m),
			CacheWrite1hPerM: roundRate(pm.CacheWrite1h),
			OutputPerM:       roundRate(pm.Output),
			Notes:            ce.Notes,
			UpdatedAt:        ce.UpdatedAt,
		}
		if ce.BaseFound {
			bpm := ce.BaseRate.PerMillion()
			view.BaseSource = ce.BaseSource
			view.BaseDetail = ce.BaseDetail
			view.BaseInputPerM = ptrFloat(bpm.Input)
			view.BaseCachedInputPerM = ptrFloat(bpm.CachedInput)
			view.BaseCacheWrite5mPerM = ptrFloat(bpm.CacheWrite5m)
			view.BaseCacheWrite1hPerM = ptrFloat(bpm.CacheWrite1h)
			view.BaseOutputPerM = ptrFloat(bpm.Output)
		}
		catalogViews = append(catalogViews, view)
	}

	if overrides == nil {
		overrides = []protocol.PricingOverrideRecord{}
	}

	writeJSON(w, http.StatusOK, protocol.PricingResponse{
		Sources:        sources,
		ObservedModels: observedViews,
		Catalog:        catalogViews,
		Overrides:      overrides,
	})
}

func (s *Server) handleAdminUpsertPricingOverride(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model            string  `json:"model"`
		InputPerM        float64 `json:"input_per_m"`
		CachedInputPerM  float64 `json:"cached_input_per_m"`
		CacheWrite5mPerM float64 `json:"cache_write_5m_per_m"`
		CacheWrite1hPerM float64 `json:"cache_write_1h_per_m"`
		OutputPerM       float64 `json:"output_per_m"`
		Notes            string  `json:"notes"`
		Recalculate      *bool   `json:"recalculate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}
	model := strings.ToLower(strings.TrimSpace(body.Model))
	if model == "" {
		writeError(w, http.StatusBadRequest, "model is required")
		return
	}
	for name, val := range map[string]float64{
		"input_per_m":          body.InputPerM,
		"cached_input_per_m":   body.CachedInputPerM,
		"cache_write_5m_per_m": body.CacheWrite5mPerM,
		"cache_write_1h_per_m": body.CacheWrite1hPerM,
		"output_per_m":         body.OutputPerM,
	} {
		if math.IsNaN(val) || math.IsInf(val, 0) || val < 0 {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("%s must be a non-negative finite number", name))
			return
		}
	}

	saved, err := s.store.UpsertPricingOverride(r.Context(), protocol.PricingOverrideRecord{
		Model:            model,
		InputPerM:        body.InputPerM,
		CachedInputPerM:  body.CachedInputPerM,
		CacheWrite5mPerM: body.CacheWrite5mPerM,
		CacheWrite1hPerM: body.CacheWrite1hPerM,
		OutputPerM:       body.OutputPerM,
		Notes:            body.Notes,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.pricing.SetOverride(pricing.OverrideEntry{
		Model:     saved.Model,
		Rate:      pricing.FromPerMillion(saved.InputPerM, saved.CachedInputPerM, saved.CacheWrite5mPerM, saved.CacheWrite1hPerM, saved.OutputPerM),
		Notes:     saved.Notes,
		UpdatedAt: saved.UpdatedAt,
	})

	shouldRecalc := body.Recalculate == nil || *body.Recalculate
	var updatedEvents, updatedSessions int64
	if shouldRecalc {
		updatedEvents, updatedSessions, err = s.RecalculateUsageCosts(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("saved override, but recalculate failed: %v", err))
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"override":         saved,
		"recalculated":     shouldRecalc,
		"updated_events":   updatedEvents,
		"updated_sessions": updatedSessions,
	})
}

func (s *Server) handleAdminDeletePricingOverride(w http.ResponseWriter, r *http.Request) {
	model := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("model")))
	if model == "" {
		writeError(w, http.StatusBadRequest, "model query parameter is required")
		return
	}
	if err := s.store.DeletePricingOverride(r.Context(), model); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "pricing override not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.pricing.DeleteOverride(model)

	shouldRecalc := r.URL.Query().Get("recalculate") != "false" && r.URL.Query().Get("recalculate") != "0"
	var updatedEvents, updatedSessions int64
	if shouldRecalc {
		var err error
		updatedEvents, updatedSessions, err = s.RecalculateUsageCosts(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("deleted override, but recalculate failed: %v", err))
			return
		}
		// If Hermes had archived files and this model may have used Hermes-reported costs,
		// re-parse Hermes files so provider-reported costs are restored cleanly.
		if files, ferr := s.store.ListAllRawFiles(r.Context()); ferr == nil {
			for _, f := range files {
				if f.Source == protocol.SourceHermes {
					_ = s.ParseFileNow(r.Context(), f.ID)
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":           "deleted",
		"model":            model,
		"recalculated":     shouldRecalc,
		"updated_events":   updatedEvents,
		"updated_sessions": updatedSessions,
	})
}

func (s *Server) handleAdminRecalculatePricing(w http.ResponseWriter, r *http.Request) {
	updatedEvents, updatedSessions, err := s.RecalculateUsageCosts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"updated_events":   updatedEvents,
		"updated_sessions": updatedSessions,
	})
}
