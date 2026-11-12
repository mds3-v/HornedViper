package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"hornedviper/internal/domain"
	"hornedviper/internal/platform"
)

// handleAutoCTF fetches all unsolved CTFd challenges, creates one task per
// challenge with a per-task CTF config, and starts the normal solve loop.
func (s *Server) handleAutoCTF(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/tasks", http.StatusSeeOther)
		return
	}
	url, token, err := parseCTFPlatformConfig(r.FormValue("ctf_config"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if url == "" || token == "" {
		http.Error(w, "自动 CTF 需要 CTFd 地址和 Token", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	client := platform.NewCTFd(url, token)
	challenges, err := client.List(ctx)
	if err != nil {
		http.Error(w, "拉取题目失败: "+err.Error(), http.StatusBadGateway)
		return
	}
	sort.SliceStable(challenges, func(i, j int) bool {
		if challenges[i].Solved != challenges[j].Solved {
			return !challenges[i].Solved
		}
		return challenges[i].Value < challenges[j].Value
	})
	created := 0
	for _, ch := range challenges {
		if ch.Solved {
			continue
		}
		external := strconv.FormatInt(ch.ID, 10)
		if existing, _ := s.st.TaskIDForExternal(ctx, "ctfd", external); existing > 0 {
			continue
		}
		desc := strings.TrimSpace(ch.Description)
		if len(desc) > 20000 {
			desc = desc[:20000] + "\n…"
		}
		goal := fmt.Sprintf("CTFd 题目 #%d：%s\n分类：%s\n分值：%d\n\n%s", ch.ID, ch.Name, ch.Category, ch.Value, desc)
		budget := map[string]any{
			"ctf": map[string]any{
				"platform": "ctfd", "url": url, "token": token, "challenge_id": ch.ID,
			},
		}
		task, err := s.StartTaskWithBudget(ctx, ch.Name, domain.ModeCTF, goal, budget)
		if err != nil {
			log.Printf("[auto-ctf] create task for challenge %d: %v", ch.ID, err)
			continue
		}
		if err := s.st.LinkExternalTask(ctx, "ctfd", external, task.ID); err != nil {
			log.Printf("[auto-ctf] link task %d to challenge %d: %v", task.ID, ch.ID, err)
			continue
		}
		created++
	}
	log.Printf("[auto-ctf] fetched %d challenges, created %d tasks", len(challenges), created)
	http.Redirect(w, r, fmt.Sprintf("/tasks?synced=%d", created), http.StatusSeeOther)
}
