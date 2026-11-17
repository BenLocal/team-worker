package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/benlocal/team-worker/internal/model"
)

const protocolSetting = "coordination_protocol_version"

// PublishProtocol notifies existing runs once per revision, including across restarts.
func (s *Store) PublishProtocol(ctx context.Context, version string) error {
	if b, err := hex.DecodeString(version); err != nil || len(b) != 32 {
		return invalidf("Protocol version must be a SHA-256 digest")
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		var current string
		if err := tx.QueryRowContext(ctx, "SELECT coalesce((SELECT value FROM settings WHERE name=?),'')", protocolSetting).Scan(&current); err != nil {
			return err
		}
		if current == version {
			return nil
		}
		rows, err := tx.QueryContext(ctx, "SELECT "+agentColumns+" FROM agents WHERE status=?", model.AgentRunning)
		if err != nil {
			return err
		}
		agents := []model.Agent{}
		for rows.Next() {
			a, err := scanAgent(rows)
			if err != nil {
				rows.Close()
				return err
			}
			agents = append(agents, a)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		stamp := s.timestamp()
		body, err := json.Marshal(model.ProtocolState{Version: version, OfferedAt: stamp})
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE agents SET protocol=? WHERE status=?", string(body), model.AgentRunning); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO settings(name,value) VALUES (?,?) ON CONFLICT(name) DO UPDATE SET value=excluded.value", protocolSetting, version); err != nil {
			return err
		}
		for _, a := range agents {
			content := fmt.Sprintf("Coordination protocol updated to %s. Keep this run and its task ownership. Read the full current context with `team-worker agent protocol show %s %s --run-id %s`, then acknowledge that revision with `team-worker agent protocol ack %s %s --run-id %s --version %s`. Acknowledgment records your declaration of reading; actual task reports must follow the protocol.", version, a.Team, a.Name, a.RunID, a.Team, a.Name, a.RunID, version)
			if err := deliver(ctx, tx, a.Team, a.Name, model.Message{Type: "message", From: "team-worker", To: a.Name, Content: content, Timestamp: stamp, RequestID: randomHex(12)}); err != nil {
				return err
			}
		}
		return nil
	})
}

// RecordProtocol binds read and acknowledgment records to the current run and revision.
func (s *Store) RecordProtocol(ctx context.Context, team, name string, in model.ProtocolRequest) (*model.ProtocolState, error) {
	if b, err := hex.DecodeString(in.Version); err != nil || len(b) != 32 {
		return nil, invalidf("Protocol version must be a SHA-256 digest")
	}
	if in.Action != "read" && in.Action != "ack" {
		return nil, invalidf("Protocol action must be read or ack")
	}
	var state *model.ProtocolState
	err := s.write(ctx, func(tx *sql.Tx) error {
		a, err := getAgent(ctx, tx, team, name)
		if err != nil {
			return err
		}
		if in.RunID == "" || a.RunID != in.RunID || a.Status != model.AgentRunning {
			return conflictf("Protocol report must name the current running instance")
		}
		if a.Protocol == nil {
			a.Protocol = &model.ProtocolState{Version: in.Version, OfferedAt: s.timestamp()}
		}
		state = a.Protocol
		if state.Version != in.Version {
			return conflictf("Protocol revision changed; read the current context again")
		}
		switch in.Action {
		case "read":
			if state.ReadAt != "" {
				return nil
			}
			state.ReadAt = s.timestamp()
		case "ack":
			if state.ReadAt == "" {
				return conflictf("Read the current protocol before acknowledging it")
			}
			if state.AckedAt != "" {
				return nil
			}
			state.AckedAt = s.timestamp()
		}
		body, err := json.Marshal(state)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE agents SET protocol=? WHERE team=? AND name=?", string(body), team, name)
		return err
	})
	return state, err
}
