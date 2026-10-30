package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/benlocal/team-worker/internal/model"
)

type JoinRequestInput struct {
	ProposedName string `json:"proposedName"`
	User         string `json:"user"`
	Capabilities string `json:"capabilities"`
}

type JoinDecisionInput struct {
	Caller       string `json:"caller"`
	CallerUser   string `json:"callerUser"`
	AssignedName string `json:"assignedName"`
	Reason       string `json:"reason"`
}

const joinColumns = `id, team, proposed_name, user_name, capabilities, status, assigned_name, agent_id,
	reason, decided_by, created_at, decided_at`

func scanJoinRequest(row scanner) (model.JoinRequest, error) {
	var r model.JoinRequest
	err := row.Scan(&r.RequestID, &r.TeamName, &r.ProposedName, &r.User, &r.Capabilities, &r.Status,
		&r.AssignedName, &r.AgentID, &r.Reason, &r.DecidedBy, &r.CreatedAt, &r.DecidedAt)
	return r, err
}

// RequestJoin records a pending request and tells the leader about it.
func (s *Store) RequestJoin(ctx context.Context, team string, in JoinRequestInput) (model.JoinRequest, error) {
	if err := validate(in.ProposedName, "proposed name"); err != nil {
		return model.JoinRequest{}, err
	}
	if err := validateOptional(in.User, "user name"); err != nil {
		return model.JoinRequest{}, err
	}
	r := model.JoinRequest{
		RequestID:    "join-" + randomHex(12),
		TeamName:     team,
		ProposedName: in.ProposedName,
		User:         in.User,
		Capabilities: in.Capabilities,
		Status:       model.JoinPending,
		CreatedAt:    s.timestamp(),
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := requireTeam(ctx, tx, team); err != nil {
			return err
		}
		leader, err := joinLeader(ctx, tx, team)
		if err != nil {
			return err
		}
		if err := requireNotMember(ctx, tx, team, in.ProposedName, in.User); err != nil {
			return err
		}
		var one int
		err = tx.QueryRowContext(ctx,
			"SELECT 1 FROM join_requests WHERE team = ? AND proposed_name = ? AND user_name = ? AND status = 'pending'",
			team, in.ProposedName, in.User).Scan(&one)
		if err == nil {
			return conflictf("A join request for '%s' is already pending", in.ProposedName)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO join_requests (team, id, proposed_name, user_name, capabilities, status, created_at)
			 VALUES (?, ?, ?, ?, ?, 'pending', ?)`,
			team, r.RequestID, r.ProposedName, r.User, r.Capabilities, r.CreatedAt); err != nil {
			return err
		}
		return deliver(ctx, tx, team, leader.InboxName(), model.Message{
			Type:         "join_request",
			From:         in.ProposedName,
			To:           leader.Name,
			RequestID:    r.RequestID,
			Timestamp:    r.CreatedAt,
			ProposedName: in.ProposedName,
			Capabilities: in.Capabilities,
		})
	})
	return r, err
}

func (s *Store) GetJoinRequest(ctx context.Context, team, id string) (model.JoinRequest, error) {
	if err := validate(team, "team name"); err != nil {
		return model.JoinRequest{}, err
	}
	return getJoinRequest(ctx, s.db, team, id)
}

func getJoinRequest(ctx context.Context, q queryer, team, id string) (model.JoinRequest, error) {
	r, err := scanJoinRequest(q.QueryRowContext(ctx,
		"SELECT "+joinColumns+" FROM join_requests WHERE team = ? AND id = ?", team, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.JoinRequest{}, notFoundf("No join request found with id '%s'", id)
	}
	return r, err
}

// ListJoinRequests returns a team's requests, oldest first, optionally of one status.
func (s *Store) ListJoinRequests(ctx context.Context, team string, status model.JoinStatus) ([]model.JoinRequest, error) {
	if err := requireTeam(ctx, s.db, team); err != nil {
		return nil, err
	}
	switch status {
	case "", model.JoinPending, model.JoinApproved, model.JoinRejected:
	default:
		return nil, invalidf("Invalid join request status '%s'", status)
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+joinColumns+" FROM join_requests WHERE team = ? AND (? = '' OR status = ?) ORDER BY created_at, id",
		team, status, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.JoinRequest{}
	for rows.Next() {
		r, err := scanJoinRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ApproveJoin adds the requester as a member and sends it its assigned name
// and agent id, all in one transaction. Only the leader may approve.
func (s *Store) ApproveJoin(ctx context.Context, team, id string, in JoinDecisionInput) (model.JoinRequest, error) {
	var r model.JoinRequest
	err := s.write(ctx, func(tx *sql.Tx) error {
		var err error
		if r, err = pendingRequestForLeader(ctx, tx, team, id, in, "approve"); err != nil {
			return err
		}
		r.AssignedName = in.AssignedName
		if r.AssignedName == "" {
			r.AssignedName = r.ProposedName
		}
		if err := validate(r.AssignedName, "assigned name"); err != nil {
			return err
		}
		if err := requireNotMember(ctx, tx, team, r.AssignedName, r.User); err != nil {
			return err
		}
		member := model.Member{Name: r.AssignedName, User: r.User, AgentID: NewAgentID(), AgentType: "general-purpose"}
		r.AgentID, r.Status, r.DecidedBy, r.DecidedAt = member.AgentID, model.JoinApproved, in.Caller, s.timestamp()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO members (team, name, user_name, agent_id, agent_type, joined_at) VALUES (?, ?, ?, ?, ?, ?)`,
			team, member.Name, member.User, member.AgentID, member.AgentType, r.DecidedAt); err != nil {
			return err
		}
		if err := saveDecision(ctx, tx, r); err != nil {
			return err
		}
		return deliver(ctx, tx, team, member.InboxName(), model.Message{
			Type:         "join_approved",
			From:         in.Caller,
			To:           member.Name,
			RequestID:    r.RequestID,
			Timestamp:    r.DecidedAt,
			AssignedName: member.Name,
			AgentID:      member.AgentID,
			TeamName:     team,
		})
	})
	return r, err
}

// RejectJoin marks a pending request rejected. The requester is not a member
// and has no inbox, so join-status reads the stored reason. Only the leader
// may reject.
func (s *Store) RejectJoin(ctx context.Context, team, id string, in JoinDecisionInput) (model.JoinRequest, error) {
	var r model.JoinRequest
	err := s.write(ctx, func(tx *sql.Tx) error {
		var err error
		if r, err = pendingRequestForLeader(ctx, tx, team, id, in, "reject"); err != nil {
			return err
		}
		r.Status, r.Reason, r.DecidedBy, r.DecidedAt = model.JoinRejected, in.Reason, in.Caller, s.timestamp()
		return saveDecision(ctx, tx, r)
	})
	return r, err
}

func pendingRequestForLeader(ctx context.Context, tx *sql.Tx, team, id string, in JoinDecisionInput, action string) (model.JoinRequest, error) {
	if err := requireTeam(ctx, tx, team); err != nil {
		return model.JoinRequest{}, err
	}
	leader, err := joinLeader(ctx, tx, team)
	if err != nil {
		return model.JoinRequest{}, err
	}
	if in.Caller != leader.Name || (leader.User != "" && in.CallerUser != leader.User) {
		return model.JoinRequest{}, invalidf("Only the team leader '%s' can %s join requests", leader.Name, action)
	}
	r, err := getJoinRequest(ctx, tx, team, id)
	if err != nil {
		return model.JoinRequest{}, err
	}
	if r.Status != model.JoinPending {
		return model.JoinRequest{}, conflictf("Join request '%s' is already %s", id, r.Status)
	}
	return r, nil
}

func saveDecision(ctx context.Context, tx *sql.Tx, r model.JoinRequest) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE join_requests SET status = ?, assigned_name = ?, agent_id = ?, reason = ?, decided_by = ?, decided_at = ?
		 WHERE team = ? AND id = ?`,
		r.Status, r.AssignedName, r.AgentID, r.Reason, r.DecidedBy, r.DecidedAt, r.TeamName, r.RequestID)
	return err
}

// joinLeader is the member holding the team's lead agent id, else the first member.
func joinLeader(ctx context.Context, q queryer, team string) (model.Member, error) {
	t, err := getTeam(ctx, q, team)
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
	return model.Member{}, notFoundf("No leader found")
}

func requireNotMember(ctx context.Context, q queryer, team, name, user string) error {
	var one int
	err := q.QueryRowContext(ctx, "SELECT 1 FROM members WHERE team = ? AND name = ? AND user_name = ?",
		team, name, user).Scan(&one)
	if err == nil {
		return conflictf("'%s' is already a member of team '%s'", name, team)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}
