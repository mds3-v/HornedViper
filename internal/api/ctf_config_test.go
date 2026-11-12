package api

import "testing"

func TestParseNaturalCTFConfig(t *testing.T) {
	cfg, err := parseCTFConfig("CTFd 地址：https://ctfd.example.com\nToken：abc123\n题目 ID：42")
	if err != nil {
		t.Fatal(err)
	}
	if cfg["url"] != "https://ctfd.example.com" || cfg["token"] != "abc123" || cfg["challenge_id"] != int64(42) {
		t.Fatalf("cfg = %+v", cfg)
	}
	if _, err := parseCTFConfig("CTFd 地址：https://ctfd.example.com"); err == nil {
		t.Fatal("missing token should fail")
	}
}
