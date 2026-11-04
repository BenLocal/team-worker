package store

import (
	"context"
	"database/sql"
	"encoding/json"
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
	// Repos are the team's default repositories for worktrees.
	Repos []string `json:"repos,omitempty"`
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
		repos, err := json.Marshal(nonNil(in.Repos))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO teams (name, description, lead_agent_id, created_at, repos) VALUES (?, ?, ?, ?, ?)",
			in.Name, in.Description, in.LeaderID, now, string(repos)); err != nil {
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
	var repos string
	err := q.QueryRowContext(ctx,
		"SELECT description, lead_agent_id, created_at, repos FROM teams WHERE name = ?", name,
	).Scan(&t.Description, &t.LeadAgentID, &t.CreatedAt, &repos)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Team{}, notFoundf("Team '%s' not found", name)
	}
	if err != nil {
		return model.Team{}, err
	}
	if err := json.Unmarshal([]byte(repos), &t.Repos); err != nil {
		return model.Team{}, err
	}
	t.Members, err = listMembers(ctx, q, name)
	return t, err
}

// SetTeamRepos replaces the team's default repositories for worktrees.
func (s *Store) SetTeamRepos(ctx context.Context, team string, repos []string) (model.Team, error) {
	data, err := json.Marshal(nonNil(repos))
	if err != nil {
		return model.Team{}, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		if err := requireTeam(ctx, tx, team); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE teams SET repos = ? WHERE name = ?", string(data), team)
		return err
	})
	if err != nil {
		return model.Team{}, err
	}
	return s.GetTeam(ctx, team)
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

func listMembers(ctx context.Context, q queryer, team string) ([]model.Member, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT name, user_name, agent_id, agent_type, joined_at, plan_required
		 FROM members WHERE team = ? ORDER BY seq`, team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []model.Member{}
	for rows.Next() {
		var m model.Member
		if err := rows.Scan(&m.Name, &m.User, &m.AgentID, &m.AgentType, &m.JoinedAt, &m.PlanRequired); err != nil {
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

// Overview summarizes every team: leader, member and task counts and the
// messages waiting in its inboxes.
func (s *Store) Overview(ctx context.Context) ([]model.TeamOverview, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT t.name, t.description,
		        COALESCE((SELECT m.name FROM members m WHERE m.team = t.name AND m.agent_id = t.lead_agent_id
		                  ORDER BY m.seq LIMIT 1), ''),
		        (SELECT COUNT(*) FROM members m WHERE m.team = t.name),
		        (SELECT COUNT(*) FROM tasks k WHERE k.team = t.name),
		        (SELECT COUNT(*) FROM messages g WHERE g.team = t.name)
		 FROM teams t ORDER BY t.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.TeamOverview{}
	for rows.Next() {
		var o model.TeamOverview
		if err := rows.Scan(&o.Name, &o.Description, &o.Leader, &o.Members, &o.Tasks, &o.PendingMessages); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

type AddMemberInput struct {
	Name      string `json:"name"`
	AgentID   string `json:"agentId"`
	AgentType string `json:"agentType"`
	User      string `json:"user"`
	// PlanRequired makes the member wait for an approved plan before working on tasks.
	PlanRequired bool `json:"planRequired,omitempty"`
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
		JoinedAt: s.timestamp(), PlanRequired: in.PlanRequired}
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
			`INSERT INTO members (team, name, user_name, agent_id, agent_type, joined_at, plan_required)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			team, m.Name, m.User, m.AgentID, m.AgentType, m.JoinedAt, m.PlanRequired)
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

// Leaders returns every team's leader name, chosen as Leader does, in one
// query. A team without members is left out.
func (s *Store) Leaders(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.name, t.lead_agent_id, m.name, m.agent_id
		FROM teams t JOIN members m ON m.team = t.name ORDER BY m.seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, lead := map[string]string{}, map[string]bool{}
	for rows.Next() {
		var team, leadID, name, agentID string
		if err := rows.Scan(&team, &leadID, &name, &agentID); err != nil {
			return nil, err
		}
		if _, ok := out[team]; !ok || (!lead[team] && agentID == leadID) {
			out[team], lead[team] = name, agentID == leadID
		}
	}
	return out, rows.Err()
}
