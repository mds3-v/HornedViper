package api

import "testing"

func TestParseHTTPCall(t *testing.T) {
	cases := []struct {
		tool, request, method, host string
		ok                          bool
	}{
		{"WebFetch", `{"url":"https://demo.local/login"}`, "GET", "demo.local", true},
		{"Bash", `{"command":"curl -X POST https://demo.local/api/login -d x=1"}`, "POST", "demo.local", true},
		{"Bash", `{"command":"wget http://10.0.0.1/a.txt"}`, "GET", "10.0.0.1", true},
		{"Bash", `{"command":"ls -la"}`, "", "", false},
		{"record_facts", `{"url":"https://demo.local"}`, "", "", false},
	}
	for _, c := range cases {
		method, host, _, ok := parseHTTPCall(c.tool, c.request)
		if ok != c.ok || method != c.method || host != c.host {
			t.Fatalf("%s %s => %s %s %v", c.tool, c.request, method, host, ok)
		}
	}
}
