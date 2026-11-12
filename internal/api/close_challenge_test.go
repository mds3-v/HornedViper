package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"hornedviper/internal/domain"
)

func TestCloseChallengeUsesTaskBenchmarkConfig(t *testing.T) {
	gotPath, gotToken := "", ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("BENCHMARK_TOKEN")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"closed":true}`))
	}))
	defer srv.Close()
	s := &Server{}
	task := &domain.Task{Budget: map[string]any{"ctf": map[string]any{
		"platform": "benchmark", "url": srv.URL, "token": "tok",
	}}}
	if err := s.closeChallenge(context.Background(), task, "challenge:b-01"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/openapi/v1/challenges/close" || gotToken != "tok" {
		t.Fatalf("path=%q token=%q", gotPath, gotToken)
	}
}
