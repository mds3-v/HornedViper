package api

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	ctfURLPattern       = regexp.MustCompile(`https?://[^\s，,；;]+`)
	ctfTokenPattern     = regexp.MustCompile(`(?i)(?:token|令牌|密钥|api[_ -]?key)\s*[:：=]\s*([A-Za-z0-9._~+/=-]+)`)
	ctfChallengePattern = regexp.MustCompile(`(?i)(?:challenge[_\s-]?id|题目\s*id|题号|challenge|题目)\s*[:：=#]?\s*(\d+)`)
)

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

// parseCTFPlatformConfig extracts CTFd URL and token from natural language or
// JSON. Auto CTF uses this once, then copies the values into each task.
func parseCTFPlatformConfig(raw string) (url, token string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("CTF 配置不能为空")
	}
	if strings.HasPrefix(raw, "{") {
		var cfg map[string]any
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return "", "", fmt.Errorf("CTF 配置不是合法 JSON: %w", err)
		}
		platform := strings.ToLower(strings.TrimSpace(stringValue(cfg["platform"])))
		if platform != "" && platform != "ctfd" {
			return "", "", fmt.Errorf("暂不支持的 CTF 平台 %q（当前支持 ctfd）", platform)
		}
		return strings.TrimRight(strings.TrimSpace(stringValue(cfg["url"])), "/"),
			strings.TrimSpace(stringValue(cfg["token"])), nil
	}
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "rctf") {
		return "", "", fmt.Errorf("暂不支持的 CTF 平台 rctf（当前支持 ctfd）")
	}
	if m := ctfURLPattern.FindString(raw); m != "" {
		url = strings.TrimRight(m, "。.,，;；)）]】")
	}
	if m := ctfTokenPattern.FindStringSubmatch(raw); len(m) == 2 {
		token = m[1]
	}
	return strings.TrimRight(url, "/"), token, nil
}

func parseCTFConfig(raw string) (map[string]any, error) {
	url, token, err := parseCTFPlatformConfig(raw)
	if err != nil {
		return nil, err
	}
	if url == "" {
		return nil, fmt.Errorf("没有识别到 CTFd 地址，请写成：CTFd 地址：https://...")
	}
	if token == "" {
		return nil, fmt.Errorf("没有识别到 Token，请写成：Token：...")
	}
	challengeID := int64(0)
	if m := ctfChallengePattern.FindStringSubmatch(raw); len(m) == 2 {
		challengeID, _ = strconv.ParseInt(m[1], 10, 64)
	}
	if challengeID <= 0 {
		return nil, fmt.Errorf("没有识别到题目 ID，请写成：题目 ID：123")
	}
	return map[string]any{"platform": "ctfd", "url": url, "token": token, "challenge_id": challengeID}, nil
}
