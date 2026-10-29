package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/benlocal/team-worker/internal/model"
)

func validate(value, kind string) error {
	if err := model.ValidateIdentifier(value, kind, false); err != nil {
		return invalidf("%s", err)
	}
	return nil
}

func validateOptional(value, kind string) error {
	if err := model.ValidateIdentifier(value, kind, true); err != nil {
		return invalidf("%s", err)
	}
	return nil
}

type CreateTeamInput struct {
	Name            string `json:"name"`
	Description     string `json:"description"`
	LeaderName      string `json:"leaderName"`
	LeaderID        string `json:"leaderId"`
	User            string `json:"user"`
	LeaderAgentType string `json:"leaderAgentType"`
}

func (s *Store) CreateTeam(ctx context.Context, in CreateTeamInput) (model.Team, error) {
	if err := validate(in.Name, "team name"); err != nil {
		return model.Team{}, err
	}
	if err := validate(in.LeaderName, "leader name"); err != nil {
		return model.Team{}, err
	}
	if err := validateOptional(in.User, "user name"); err != nil {
		return model.Team{}, err
	}
	if in.LeaderID == "" {
		in.LeaderID = NewAgentID()
	}
	if in.LeaderAgentType == "" {
		in.LeaderAgentType = "leader"
	}
	now := s.timestamp()
	err := s.write(ctx, func(tx *sql.Tx) error {
		var one int
		err := tx.QueryRowContext(ctx, "SELECT 1 FROM teams WHERE name = ?", in.Name).Scan(&one)
		if err == nil {
			return conflictf("Team '%s' already exists", in.Name)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO teams (name, description, lead_agent_id, created_at) VALUES (?, ?, ?, ?)",
			in.Name, in.Description, in.LeaderID, now); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO members (team, name, user_name, agent_id, agent_type, joined_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			in.Name, in.LeaderName, in.User, in.LeaderID, in.LeaderAgentType, now)
		return err
	})
	if err != nil {
		return model.Team{}, err
	}
	return s.GetTeam(ctx, in.Name)
}

func (s *Store) GetTeam(ctx context.Context, name string) (model.Team, error) {
	return getTeam(ctx, s.db, name)
}

func getTeam(ctx context.Context, q queryer, name string) (model.Team, error) {
	if err := validate(name, "team name"); err != nil {
		return model.Team{}, err
	}
	t := model.Team{Name: name}
	err := q.QueryRowContext(ctx,
		"SELECT description, lead_agent_id, created_at FROM teams WHERE name = ?", name,
	).Scan(&t.Description, &t.LeadAgentID, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Team{}, notFoundf("Team '%s' not found", name)
	}
	if err != nil {
		return model.Team{}, err
	}
	t.Members, err = listMembers(ctx, q, name)
	return t, err
}

func listMembers(ctx context.Context, q queryer, team string) ([]model.Member, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT name, user_name, agent_id, agent_type, joined_at
		 FROM members WHERE team = ? ORDER BY seq`, team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []model.Member{}
	for rows.Next() {
		var m model.Member
		if err := rows.Scan(&m.Name, &m.User, &m.AgentID, &m.AgentType, &m.JoinedAt); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

func (s *Store) ListTeams(ctx context.Context) ([]model.TeamSummary, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT t.name, t.description, t.lead_agent_id,
		        (SELECT COUNT(*) FROM members m WHERE m.team = t.name)
		 FROM teams t ORDER BY t.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	teams := []model.TeamSummary{}
	for rows.Next() {
		var t model.TeamSummary
		if err := rows.Scan(&t.Name, &t.Description, &t.LeadAgentID, &t.MemberCount); err != nil {
			return nil, err
		}
		teams = append(teams, t)
	}
	return teams, rows.Err()
}

type AddMemberInput struct {
	Name      string `json:"name"`
	AgentID   string `json:"agentId"`
	AgentType string `json:"agentType"`
	User      string `json:"user"`
}

func (s *Store) AddMember(ctx context.Context, team string, in AddMemberInput) (model.Member, error) {
	if err := validate(in.Name, "member name"); err != nil {
		return model.Member{}, err
	}
	if err := validateOptional(in.User, "user name"); err != nil {
		return model.Member{}, err
	}
	if in.AgentID == "" {
		in.AgentID = NewAgentID()
	}
	if in.AgentType == "" {
		in.AgentType = "general-purpose"
	}
	m := model.Member{Name: in.Name, User: in.User, AgentID: in.AgentID, AgentType: in.AgentType,
		JoinedAt: s.timestamp()}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := requireTeam(ctx, tx, team); err != nil {
			return err
		}
		var one int
		err := tx.QueryRowContext(ctx,
			"SELECT 1 FROM members WHERE team = ? AND name = ? AND user_name = ?",
			team, in.Name, in.User).Scan(&one)
		if err == nil {
			user := in.User
			if user == "" {
				user = "(none)"
			}
			return conflictf("Agent '%s' (user=%s) already in team", in.Name, user)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO members (team, name, user_name, agent_id, agent_type, joined_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			team, m.Name, m.User, m.AgentID, m.AgentType, m.JoinedAt)
		return err
	})
	return m, err
}

func (s *Store) RemoveMember(ctx context.Context, team, name string) (bool, error) {
	var removed bool
	err := s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM members WHERE team = ? AND name = ?", team, name)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		removed = n > 0
		return err
	})
	return removed, err
}

// DeleteTeam removes a team and, through cascades, its members, tasks and messages.
func (s *Store) DeleteTeam(ctx context.Context, name string) (bool, error) {
	if err := validate(name, "team name"); err != nil {
		return false, err
	}
	var deleted bool
	err := s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM teams WHERE name = ?", name)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		deleted = n > 0
		return err
	})
	return deleted, err
}

// Leader returns the member whose agent id matches the team's lead agent,
// falling back to the first member.
func (s *Store) Leader(ctx context.Context, team string) (model.Member, error) {
	t, err := s.GetTeam(ctx, team)
	if err != nil {
		return model.Member{}, err
	}
	for _, m := range t.Members {
		if m.AgentID == t.LeadAgentID {
			return m, nil
		}
	}
	if len(t.Members) > 0 {
		return t.Members[0], nil
	}
	return model.Member{}, notFoundf("Team '%s' has no members", team)
}

func (s *Store) TeamPrompt(ctx context.Context, team string) (string, error) {
	if err := validate(team, "team name"); err != nil {
		return "", err
	}
	var prompt string
	err := s.db.QueryRowContext(ctx, "SELECT prompt FROM teams WHERE name = ?", team).Scan(&prompt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", notFoundf("Team '%s' not found", team)
	}
	return prompt, err
}

const maxTeamPromptLength = 65536

// SetTeamPrompt saves shared instructions and reports whether they changed.
func (s *Store) SetTeamPrompt(ctx context.Context, team, prompt string) (bool, error) {
	if len(prompt) > maxTeamPromptLength {
		return false, invalidf("Prompt must be text of at most %d characters", maxTeamPromptLength)
	}
	var changed bool
	err := s.write(ctx, func(tx *sql.Tx) error {
		var previous string
		err := tx.QueryRowContext(ctx, "SELECT prompt FROM teams WHERE name = ?", team).Scan(&previous)
		if errors.Is(err, sql.ErrNoRows) {
			return notFoundf("Team '%s' not found", team)
		}
		if err != nil || previous == prompt {
			return err
		}
		changed = true
		_, err = tx.ExecContext(ctx, "UPDATE teams SET prompt = ? WHERE name = ?", prompt, team)
		return err
	})
	return changed, err
}

// resolveInbox maps a logical agent name to its inbox key. With a user it
// prefers that user's member; otherwise a unique name match wins. Unknown
// names are used as-is, so messages to agents that have not joined still land.
func resolveInbox(ctx context.Context, q queryer, team, name, user string) (string, error) {
	if err := validate(name, "recipient name"); err != nil {
		return "", err
	}
	if err := validateOptional(user, "user name"); err != nil {
		return "", err
	}
	members, err := listMembers(ctx, q, team)
	if err != nil {
		return "", err
	}
	if user != "" {
		for _, m := range members {
			if m.Name == name && m.User == user {
				return m.InboxName(), nil
			}
		}
	}
	var match *model.Member
	for i := range members {
		if members[i].Name == name {
			if match != nil {
				return name, nil
			}
			match = &members[i]
		}
	}
	if match != nil {
		return match.InboxName(), nil
	}
	return name, nil
}
