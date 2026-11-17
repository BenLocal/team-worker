package server

import (
	"net/http"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/runner"
)

func (s *Server) agentProtocol(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	team, name := r.PathValue("team"), r.PathValue("name")
	a, err := s.store.GetAgent(ctx, team, name)
	if err != nil {
		respond(w, nil, err)
		return
	}
	t, err := s.store.GetTeam(ctx, team)
	if err != nil {
		respond(w, nil, err)
		return
	}
	var member model.Member
	var leader string
	for _, m := range t.Members {
		if m.AgentID == t.LeadAgentID {
			leader = m.Name
		}
		if m.AgentID == a.AgentID || (a.AgentID == "" && m.Name == name) {
			member = m
		}
	}
	teamPrompt, err := s.store.TeamPrompt(ctx, team)
	if err != nil {
		respond(w, nil, err)
		return
	}
	agentPrompt, err := s.store.AgentPrompt(ctx, team, name, member.User)
	if err != nil {
		respond(w, nil, err)
		return
	}
	out := model.ProtocolContext{Version: runner.CoordinationVersion(), Content: runner.CoordinationContext(a, member, leader, teamPrompt, agentPrompt), State: a.Protocol}
	if r.Method == http.MethodPost {
		var in model.ProtocolRequest
		if !decode(w, r, &in) {
			return
		}
		if in.Version != out.Version {
			writeError(w, http.StatusConflict, "Protocol revision changed; read the current context again")
			return
		}
		out.State, err = s.store.RecordProtocol(ctx, team, name, in)
	}
	respond(w, out, err)
}
