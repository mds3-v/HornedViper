package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCTFdListAndSubmit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token t" {
			t.Fatalf("auth = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/api/v1/challenges":
			_, _ = w.Write([]byte(`{"success":true,"data":[{"id":7,"name":"web","category":"web","value":100,"solved_by_me":false}]}`))
		case "/api/v1/challenges/attempt":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["submission"] != "flag{x}" {
				t.Fatalf("body = %+v", body)
			}
			_, _ = w.Write([]byte(`{"success":true,"data":{"status":"correct","message":"Correct"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewCTFd(srv.URL, "t")
	list, err := c.List(context.Background())
	if err != nil || len(list) != 1 || list[0].ID != 7 {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	res, err := c.Submit(context.Background(), 7, "flag{x}")
	if err != nil || !res.Accepted {
		t.Fatalf("submit=%+v err=%v", res, err)
	}
}
