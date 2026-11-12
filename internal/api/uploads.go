package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"hornedviper/internal/domain"
)

func (s *Server) saveUploads(ctx context.Context, taskID int64, kind string, files []*multipart.FileHeader, relPaths []string, preserveDirs bool) ([]domain.Attachment, error) {
	return s.saveUploadsDir(ctx, fmt.Sprintf("task-%d", taskID), kind, files, relPaths, preserveDirs)
}

func (s *Server) saveUploadsDir(ctx context.Context, ownerDir, kind string, files []*multipart.FileHeader, relPaths []string, preserveDirs bool) ([]domain.Attachment, error) {
	if len(files) == 0 {
		return nil, nil
	}
	cfg := s.cfgStore.Get()
	base, err := filepath.Abs(cfg.ArtifactDir)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, ownerDir, kind)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	var out []domain.Attachment
	for i, fh := range files {
		name := strings.TrimSpace(fh.Filename)
		if preserveDirs && i < len(relPaths) && strings.TrimSpace(relPaths[i]) != "" {
			name = strings.TrimSpace(relPaths[i])
		}
		if preserveDirs {
			name = strings.ReplaceAll(name, "\\", "/")
			name = strings.TrimPrefix(name, "/")
			name = path.Clean(name)
			if name == "" || name == "." || name == ".." || strings.HasPrefix(name, "../") {
				name = fmt.Sprintf("file-%d", time.Now().UnixNano())
			}
		} else {
			name = filepath.Base(name)
			if name == "" || name == "." || name == "/" {
				name = fmt.Sprintf("file-%d", time.Now().UnixNano())
			}
		}
		dest := filepath.Join(dir, filepath.FromSlash(name))
		if _, err := os.Stat(dest); err == nil {
			ext := filepath.Ext(name)
			base := strings.TrimSuffix(name, ext)
			name = fmt.Sprintf("%s-%d%s", base, time.Now().UnixNano(), ext)
			dest = filepath.Join(dir, filepath.FromSlash(name))
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return nil, err
		}
		src, err := fh.Open()
		if err != nil {
			return nil, err
		}
		dst, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			src.Close()
			return nil, err
		}
		n, copyErr := io.Copy(dst, src)
		src.Close()
		dst.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		ct := fh.Header.Get("Content-Type")
		if ct == "" {
			ct = mime.TypeByExtension(filepath.Ext(name))
		}
		out = append(out, domain.Attachment{
			Name: filepath.ToSlash(name), Path: dest, Kind: kind, ContentType: ct, Size: n,
		})
	}
	return out, nil
}

func attachmentURL(taskID int64, kind, name string) string {
	return fmt.Sprintf("/attachments?task=%d&kind=%s&name=%s", taskID, url.QueryEscape(kind), url.QueryEscape(name))
}

func conversationAttachmentURL(convID int64, kind, name string) string {
	return fmt.Sprintf("/attachments?conversation=%d&kind=%s&name=%s", convID, url.QueryEscape(kind), url.QueryEscape(name))
}

func (s *Server) handleAttachment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	taskID := r.URL.Query().Get("task")
	convID := r.URL.Query().Get("conversation")
	kind := r.URL.Query().Get("kind")
	name := r.URL.Query().Get("name")
	if (kind != "uploads" && kind != "chat") || name == "" {
		http.NotFound(w, r)
		return
	}
	ownerDir := ""
	if convID != "" {
		if _, err := strconv.ParseInt(convID, 10, 64); err != nil {
			http.NotFound(w, r)
			return
		}
		ownerDir = "conversation-" + convID
	} else {
		if _, err := strconv.ParseInt(taskID, 10, 64); err != nil {
			http.NotFound(w, r)
			return
		}
		ownerDir = "task-" + taskID
	}
	rel := path.Clean(strings.ReplaceAll(name, "\\", "/"))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
		http.NotFound(w, r)
		return
	}
	cfg := s.cfgStore.Get()
	base, err := filepath.Abs(cfg.ArtifactDir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	filePath := filepath.Join(base, ownerDir, kind, filepath.FromSlash(rel))
	abs, err := filepath.Abs(filePath)
	if err != nil || !strings.HasPrefix(abs, filepath.Join(base, ownerDir, kind)+string(os.PathSeparator)) {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, abs)
}

func parseUploadPaths(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var paths []string
	if err := json.Unmarshal([]byte(raw), &paths); err != nil {
		return nil
	}
	return paths
}
