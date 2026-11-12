package submit

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"hornedviper/internal/domain"
)

// CommandSubmitter submits a flag by running a user-provided shell command.
//
// The command receives:
//   - HV_FLAG, HV_TASK_ID, HV_FINDING_ID in the environment
//   - the literal {flag} placeholder, replaced with a shell-quoted flag
//
// Exit code 0 means accepted; non-zero means rejected. stdout/stderr are kept
// as the submission message.
type CommandSubmitter struct {
	command string
	flagRe  *regexp.Regexp
	timeout time.Duration
}

func NewCommand(command string) *CommandSubmitter {
	return &CommandSubmitter{
		command: strings.TrimSpace(command),
		flagRe:  regexp.MustCompile(`flag\{[^}\r\n]{1,256}\}`),
		timeout: 60 * time.Second,
	}
}

func (s *CommandSubmitter) Enabled() bool {
	return s != nil && s.command != ""
}

func (s *CommandSubmitter) Submit(ctx context.Context, task *domain.Task, finding domain.Finding) (domain.SubmitResult, error) {
	if !s.Enabled() {
		return domain.SubmitResult{}, fmt.Errorf("submit: no command configured")
	}
	flag := s.extractFlag(finding.Evidence)
	if flag == "" {
		flag = s.extractFlag(finding.Title)
	}
	if flag == "" {
		return domain.SubmitResult{}, fmt.Errorf("submit: no flag found in finding %d", finding.ID)
	}

	runCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	command := s.command
	if strings.Contains(command, "{flag}") {
		command = strings.ReplaceAll(command, "{flag}", shellQuote(flag))
	}
	cmd := exec.CommandContext(runCtx, "/bin/sh", "-c", command)
	cmd.Env = append(os.Environ(),
		"HV_FLAG="+flag,
		fmt.Sprintf("HV_TASK_ID=%d", task.ID),
		fmt.Sprintf("HV_FINDING_ID=%d", finding.ID),
	)
	out, err := cmd.CombinedOutput()
	message := strings.TrimSpace(string(out))
	if err != nil {
		if message == "" {
			message = err.Error()
		}
		return domain.SubmitResult{Flag: flag, Accepted: false, Message: message}, nil
	}
	return domain.SubmitResult{Flag: flag, Accepted: true, Message: message}, nil
}

func (s *CommandSubmitter) extractFlag(text string) string {
	return s.flagRe.FindString(text)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
