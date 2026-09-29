package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/wyw14/solo-039/internal/service"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{
		runtime: service.NewRuntime("baseline"),
		plant:   service.NewPlant(t.TempDir()),
		router:  chi.NewRouter(),
	}
	s.routes()
	return s
}

func do(t *testing.T, s *Server, method, target, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	var decoded map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("response is not JSON (%d): %s", rec.Code, rec.Body.String())
		}
	}
	return rec, decoded
}

func TestTransitionFailureResponseGuidesRetry(t *testing.T) {
	s := newTestServer(t)
	if rec, _ := do(t, s, http.MethodPost, "/api/campaign/start?grade=baseline&source=primary", ""); rec.Code != http.StatusAccepted {
		t.Fatalf("start status = %d", rec.Code)
	}

	// 新来源未登记（不可用）：操作必须失败，但要给出结构化结论与重试入口。
	rec, body := do(t, s, http.MethodPost, "/api/campaign/transition", `{"grade":"premium","source":"secondary"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if body["failed_stage"] != "feed-switch" {
		t.Fatalf("unexpected conclusion: %v", body)
	}
	if body["rolled_back"] != true {
		t.Fatalf("rolled_back must be true: %v", body)
	}
	if body["safe_hold"] != false {
		t.Fatalf("safe_hold must be false on clean rollback: %v", body)
	}
	retryFrom, _ := body["retry_from"].(string)
	if retryFrom == "" {
		t.Fatal("response must tell the operator where to retry")
	}
	campaign, _ := body["campaign"].(map[string]any)
	if campaign["grade"] != "baseline" || campaign["phase"] != "stabilizing" {
		t.Fatalf("campaign not restored in response: %v", campaign)
	}
	feed, _ := body["feed"].(map[string]any)
	if feed["source"] != "primary" || feed["active"] != true {
		t.Fatalf("feed not restored in response: %v", feed)
	}

	// 登记来源后按指引重试，换品成功。
	if rec, _ = do(t, s, http.MethodPost, "/api/feed/sources", `{"source":"secondary","available":true}`); rec.Code != http.StatusAccepted {
		t.Fatalf("register source status = %d", rec.Code)
	}
	rec, body = do(t, s, http.MethodPost, "/api/campaign/transition", `{"grade":"premium","source":"secondary"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("retry status = %d, body = %v", rec.Code, body)
	}
	if body["Grade"] != "premium" {
		t.Fatalf("retry did not switch grade: %v", body)
	}
}
