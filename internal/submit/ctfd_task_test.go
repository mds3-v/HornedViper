package submit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"hornedviper/internal/domain"
	"hornedviper/internal/store"
)

func TestTaskCTFdSubmit(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"success":true,"data":{"status":"correct","message":"ok"}}`))
	}))
	defer srv.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	task, err := st.CreateTask(ctx, "web", domain.ModeCTF, map[string]any{
		"ctf": map[string]any{"platform": "ctfd", "url": srv.URL, "token": "t", "challenge_id": float64(9)},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := NewTaskCTFd(st, nil).Submit(ctx, task, domain.Finding{ID: 1, Evidence: "found flag{test_flag}"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted || res.Flag != "flag{test_flag}" {
		t.Fatalf("res = %+v", res)
	}
	if got["challenge_id"].(float64) != 9 || got["submission"] != "flag{test_flag}" {
		t.Fatalf("payload = %+v", got)
	}
}
