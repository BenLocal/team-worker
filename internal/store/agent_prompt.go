package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/benlocal/team-worker/internal/model"
)

// MaxAgentPromptLength is the maximum size of stored member instructions in bytes.
const MaxAgentPromptLength = maxTeamPromptLength

// AgentPrompt returns the instructions of one member, including unstarted members.
func (s *Store) AgentPrompt(ctx context.Context, team, name, user string) (string, error) {
	return agentPrompt(ctx, s.db, team, name, user)
}

func agentPrompt(ctx context.Context, q queryer, team, name, user string) (string, error) {
	if err := validate(team, "team name"); err != nil {
		return "", err
	}
	if err := validate(name, "agent name"); err != nil {
		return "", err
	}
	if err := validateOptional(user, "user name"); err != nil {
		return "", err
	}
	var prompt string
	err := q.QueryRowContext(ctx, "SELECT prompt FROM members WHERE team = ? AND name = ? AND user_name = ?", team, name, user).Scan(&prompt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", notFoundf("Member '%s' (user=%s) not found in team '%s'", name, user, team)
	}
	return prompt, err
}

// SetAgentPrompt saves instructions and notifies the member in the same transaction.
func (s *Store) SetAgentPrompt(ctx context.Context, team, name, user, prompt string) (bool, error) {
	if len(prompt) > MaxAgentPromptLength {
		return false, invalidf("Prompt must be text of at most %d bytes", MaxAgentPromptLength)
	}
	changed := false
	err := s.write(ctx, func(tx *sql.Tx) error {
		previous, err := agentPrompt(ctx, tx, team, name, user)
		if err != nil || previous == prompt {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE members SET prompt = ? WHERE team = ? AND name = ? AND user_name = ?", prompt, team, name, user); err != nil {
			return err
		}
		member := model.Member{Name: name, User: user}
		command := fmt.Sprintf("team-worker agent prompt show %s %s", team, name)
		if user != "" {
			command += " --user " + user
		}
		msg := model.Message{Type: "message", From: "team-worker", To: member.InboxName(), RequestID: randomHex(12), Timestamp: s.timestamp(),
			Content: fmt.Sprintf("Your agent instructions were updated. Read them with `%s` and follow them from now on.", command)}
		if err := deliver(ctx, tx, team, member.InboxName(), msg); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed && err == nil, err
}
