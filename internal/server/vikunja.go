package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

const vikunjaHookRoute = "POST /api/hooks/vikunja/{team}"
const vikunjaHookBodyLimit = 256 << 10
const vikunjaCommentCreated = "task.comment.created"

var vikunjaCommentParagraph = regexp.MustCompile(`(?i)^<p(?:[[:space:]][^>]*)?>`)

func (s *Server) vikunjaDataDir() string {
	if s.runDataDir != "" {
		return s.runDataDir
	}
	return config.DataDir()
}

func (s *Server) vikunjaHook(w http.ResponseWriter, r *http.Request) {
	team := r.PathValue("team")
	hooks, err := config.ReadVikunjaHooks(s.vikunjaDataDir())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Vikunja webhook settings are unavailable")
		return
	}
	hook := hooks[team]
	if hook.Secret == "" || model.ValidateIdentifier(team, "team", false) != nil {
		writeError(w, http.StatusNotFound, "Vikunja webhook is not configured for this team")
		return
	}
	signatures := r.Header.Values("X-Vikunja-Signature")
	if len(signatures) != 1 || len(signatures[0]) != sha256.Size*2 {
		writeError(w, http.StatusUnauthorized, "Invalid Vikunja signature")
		return
	}
	signature, err := hex.DecodeString(signatures[0])
	if err != nil {
		writeError(w, http.StatusUnauthorized, "Invalid Vikunja signature")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, vikunjaHookBodyLimit))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "Vikunja webhook body is too large")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "Could not read Vikunja webhook body")
		return
	}
	mac := hmac.New(sha256.New, []byte(hook.Secret))
	mac.Write(body)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		writeError(w, http.StatusUnauthorized, "Invalid Vikunja signature")
		return
	}
	var event struct {
		Name string          `json:"event_name"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &event) != nil || strings.TrimSpace(event.Name) == "" || len(event.Data) == 0 {
		writeError(w, http.StatusBadRequest, "Expected a Vikunja event with event_name and data")
		return
	}
	if !hook.AllowsEvent(event.Name) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var leaderPrompt string
	if event.Name == vikunjaCommentCreated && len(hook.Commands) > 0 {
		leaderPrompt = vikunjaCommentLeaderPrompt(event.Data, hook.Commands)
	}
	if err := s.store.EnqueueVikunjaHook(r.Context(), team, body, leaderPrompt); err != nil {
		respond(w, nil, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

func vikunjaCommentLeaderPrompt(data json.RawMessage, commands map[string]string) string {
	var event struct {
		Comment struct {
			Text string `json:"comment"`
		} `json:"comment"`
	}
	if json.Unmarshal(data, &event) != nil {
		return ""
	}
	comment := strings.TrimSpace(event.Comment.Text)
	// Vikunja's editor wraps ordinary rich-text comments in a paragraph.
	if paragraph := vikunjaCommentParagraph.FindStringIndex(comment); paragraph != nil {
		text, _, _ := strings.Cut(comment[paragraph[1]:], "<")
		comment = html.UnescapeString(text)
	}
	for name := range strings.FieldsSeq(comment) {
		if prompt := commands[name]; prompt != "" {
			return "Vikunja comment command " + name + "\n\n" + prompt + "\n\n"
		}
		break
	}
	return ""
}

func runVikunjaHooks(ctx context.Context, st *store.Store, log *slog.Logger) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := st.DeliverVikunjaHooks(ctx); err != nil && ctx.Err() == nil {
			log.Warn("deliver Vikunja hooks", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
