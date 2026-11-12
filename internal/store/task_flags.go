package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"hornedviper/internal/domain"
)

var flagPattern = regexp.MustCompile(`(?i)flag\{[^}]{1,1000}\}`)
var challengePattern = regexp.MustCompile(`(?i)^[a-z][0-9]?-[0-9]{2}$`)

func flagHash(flag string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(flag)))
	return hex.EncodeToString(sum[:])
}

// SyncTaskFlags extracts flags from confirmed findings and inserts missing
// rows. Existing rows (including deleted ones) are left untouched.
func (s *Store) SyncTaskFlags(ctx context.Context, taskID int64) error {
	findings, err := s.ListFindings(ctx, taskID, 5000)
	if err != nil {
		return err
	}
	facts, err := s.ListFacts(ctx, taskID, 5000)
	if err != nil {
		return err
	}
	intents, _ := s.ListIntents(ctx, taskID)
	targetByIntent := map[int64]string{}
	for _, in := range intents {
		key := challengeFromTarget(in.TargetKey)
		if key == "" {
			key = challengeCode(in.Summary)
		}
		targetByIntent[in.ID] = key
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowMS()
	flagTarget := map[string]string{}
	addFlagTarget := func(flag, target string) {
		if target != "" && validFlagText(flag) {
			flagTarget[strings.ToLower(strings.TrimSpace(flag))] = target
		}
	}
	for _, f := range findings {
		text := f.Title + "\n" + f.Evidence
		flags := flagPattern.FindAllString(text, -1)
		if len(flags) == 0 {
			continue
		}
		challenge := targetByIntent[f.IntentID]
		if challenge == "" {
			challenge = challengeCode(f.Title)
		}
		if challenge == "" {
			continue
		}
		for _, flag := range flags {
			if !validFlagText(flag) {
				continue
			}
			addFlagTarget(flag, challenge)
			_, err := s.db.ExecContext(ctx,
				`INSERT INTO task_flags
				 (task_id, challenge, flag, flag_hash, status, source, finding_id, deleted, created_at, updated_at)
				 VALUES (?, ?, ?, ?, 'found', 'finding', ?, 0, ?, ?)
				 ON CONFLICT(task_id, flag_hash) DO UPDATE SET
				   challenge=CASE WHEN excluded.challenge <> '' THEN excluded.challenge ELSE task_flags.challenge END,
				   finding_id=CASE WHEN task_flags.finding_id=0 THEN excluded.finding_id ELSE task_flags.finding_id END,
				   updated_at=excluded.updated_at`,
				taskID, challenge, flag, flagHash(flag), f.ID, now, now)
			if err != nil {
				return err
			}
		}
	}
	for _, f := range facts {
		text := f.Summary + "\n" + f.Detail + "\n" + f.Evidence
		flags := flagPattern.FindAllString(text, -1)
		if len(flags) == 0 {
			continue
		}
		challenge := targetByIntent[f.IntentID]
		if challenge == "" {
			continue
		}
		for _, flag := range flags {
			if !validFlagText(flag) {
				continue
			}
			addFlagTarget(flag, challenge)
			_, err := s.db.ExecContext(ctx,
				`INSERT INTO task_flags
				 (task_id, challenge, flag, flag_hash, status, source, finding_id, deleted, created_at, updated_at)
				 VALUES (?, ?, ?, ?, 'found', 'fact', 0, 0, ?, ?)
				 ON CONFLICT(task_id, flag_hash) DO UPDATE SET
				   challenge=CASE WHEN excluded.challenge <> '' THEN excluded.challenge ELSE task_flags.challenge END,
				   updated_at=excluded.updated_at`,
				taskID, challenge, flag, flagHash(flag), now, now)
			if err != nil {
				return err
			}
		}
	}
	// Mirror accepted auto-submissions so the Flag tab reflects the submitter.
	rows, err := s.db.QueryContext(ctx,
		`SELECT flag, accepted, finding_id FROM flag_submissions WHERE task_id=?`, taskID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var flag string
		var accepted, findingID int
		if err := rows.Scan(&flag, &accepted, &findingID); err != nil {
			return err
		}
		status := "found"
		if accepted != 0 {
			status = "accepted"
		}
		challenge := "未标注"
		if findingID > 0 {
			// Resolve via the finding's intent when possible.
			var intentID int64
			_ = s.db.QueryRowContext(ctx,
				`SELECT json_extract(payload_json,'$.intent_id') FROM exploration_nodes WHERE id=? AND task_id=? AND kind='finding'`,
				findingID, taskID).Scan(&intentID)
			if intentID > 0 {
				challenge = targetByIntent[intentID]
			}
		}
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO task_flags
			 (task_id, challenge, flag, flag_hash, status, source, finding_id, deleted, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, 'submission', ?, 0, ?, ?)
			 ON CONFLICT(task_id, flag_hash) DO UPDATE SET
			   challenge=CASE WHEN excluded.challenge <> '' AND excluded.challenge <> '未标注' THEN excluded.challenge ELSE task_flags.challenge END,
			   status=CASE WHEN excluded.status='accepted' THEN 'accepted' ELSE task_flags.status END,
			   updated_at=excluded.updated_at`,
			taskID, challenge, flag, flagHash(flag), status, findingID, now, now)
		if err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// Repair rows created by older versions that used arbitrary words from a
	// title as the challenge id (cve-2025-*, index-0, on-disk, ...). Existing
	// accepted rows are preserved; known targets win over the old label.
	existing, err := s.db.QueryContext(ctx,
		`SELECT id, challenge, flag, status, source FROM task_flags WHERE task_id=? AND deleted=0`, taskID)
	if err != nil {
		return err
	}
	type flagFix struct {
		id        int64
		challenge string
		flag      string
		target    string
		status    string
		source    string
	}
	var fixes []flagFix
	for existing.Next() {
		var f flagFix
		if err := existing.Scan(&f.id, &f.challenge, &f.flag, &f.status, &f.source); err != nil {
			existing.Close()
			return err
		}
		f.target = flagTarget[strings.ToLower(strings.TrimSpace(f.flag))]
		fixes = append(fixes, f)
	}
	if err := existing.Close(); err != nil {
		return err
	}
	for _, f := range fixes {
		switch {
		case f.target != "" && f.target != f.challenge:
			if _, err := s.db.ExecContext(ctx,
				`UPDATE task_flags SET challenge=?, updated_at=? WHERE id=?`, f.target, now, f.id); err != nil {
				return err
			}
		case !challengePattern.MatchString(f.challenge) && f.status != "accepted" && f.source != "manual":
			if _, err := s.db.ExecContext(ctx,
				`UPDATE task_flags SET deleted=1, updated_at=? WHERE id=?`, now, f.id); err != nil {
				return err
			}
		}
	}
	return nil
}

func challengeFromTarget(target string) string {
	target = strings.ToLower(strings.TrimSpace(target))
	target = strings.TrimPrefix(target, "challenge:")
	target = strings.TrimPrefix(target, "challenge/")
	if challengePattern.MatchString(target) {
		return target
	}
	if i := strings.LastIndexAny(target, ":/"); i >= 0 {
		if candidate := target[i+1:]; challengePattern.MatchString(candidate) {
			return candidate
		}
	}
	return ""
}

func challengeCode(title string) string {
	// Only structured codes like a-05, c-06, e1-01, f2-08. Words such as
	// cve-2025-3248, index-0, on-disk and whole-filesystem are not challenge ids.
	for _, field := range strings.Fields(title) {
		field = strings.ToLower(strings.Trim(field, "：:()[]{}.,;，。'\""))
		if challengePattern.MatchString(field) {
			return field
		}
	}
	return ""
}

func validFlagText(flag string) bool {
	inner := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(flag), "flag{"), "}")
	inner = strings.TrimSuffix(strings.TrimPrefix(inner, "FLAG{"), "}")
	if inner == "" || strings.Contains(inner, "...") {
		return false
	}
	if strings.ContainsAny(inner, " \t\r\n") {
		return false
	}
	lower := strings.ToLower(inner)
	if lower == "example_flag_value" || strings.HasPrefix(lower, "probe_") {
		return false
	}
	return true
}

func (s *Store) ListTaskFlags(ctx context.Context, taskID int64) ([]domain.TaskFlag, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, challenge, flag, flag_hash, status, source, finding_id, created_at, updated_at
		 FROM task_flags WHERE task_id=? AND deleted=0 ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.TaskFlag
	for rows.Next() {
		var f domain.TaskFlag
		var created, updated int64
		if err := rows.Scan(&f.ID, &f.TaskID, &f.Challenge, &f.Flag, &f.FlagHash, &f.Status, &f.Source, &f.FindingID, &created, &updated); err != nil {
			return nil, err
		}
		f.CreatedAt, f.UpdatedAt = msToTime(created), msToTime(updated)
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) AddTaskFlag(ctx context.Context, taskID int64, challenge, flag, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	flag = strings.TrimSpace(flag)
	if flag == "" {
		return nil
	}
	if status == "" {
		status = "found"
	}
	now := nowMS()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO task_flags (task_id, challenge, flag, flag_hash, status, source, deleted, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 'manual', 0, ?, ?)
		 ON CONFLICT(task_id, flag_hash) DO UPDATE SET challenge=excluded.challenge, status=excluded.status, deleted=0, updated_at=excluded.updated_at`,
		taskID, strings.TrimSpace(challenge), flag, flagHash(flag), status, now, now)
	return err
}

func (s *Store) DeleteTaskFlag(ctx context.Context, taskID, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE task_flags SET deleted=1, updated_at=? WHERE id=? AND task_id=?`, nowMS(), id, taskID)
	return err
}
