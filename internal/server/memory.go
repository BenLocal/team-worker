package server

import (
	"errors"
	"io/fs"
	"net/http"
	"sync"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/memory"
	"github.com/benlocal/team-worker/internal/model"
)

type memberMemory struct {
	once      sync.Once
	files     *memory.Manager
	reflectMu sync.Mutex
	running   map[model.MemoryIdentity]bool
	gateMu    sync.Mutex
	gates     map[model.MemoryIdentity]*sync.Mutex
}

func (s *Server) memoryFiles() *memory.Manager {
	s.memory.once.Do(func() {
		dir := s.runDataDir
		if dir == "" {
			dir = config.DataDir()
		}
		opts := config.PersonalMemory(dir)
		s.memory.files = memory.NewAtRoot(opts.Root, opts.ContextBytes)
	})
	return s.memory.files
}
func memoryIdentity(r *http.Request) model.MemoryIdentity {
	return model.MemoryIdentity{Team: r.PathValue("team"), Name: r.PathValue("name"), User: r.URL.Query().Get("user")}
}
func respondMemory(w http.ResponseWriter, out any, err error) {
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, out)
	case errors.Is(err, memory.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, memory.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, memory.ErrNotFound) || errors.Is(err, fs.ErrNotExist):
		writeError(w, http.StatusNotFound, err.Error())
	default:
		respond(w, out, err)
	}
}
func (s *Server) memoryOperation(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var in model.MemoryRequest
	if !decode(w, r, &in) {
		return
	}
	id := memoryIdentity(r)
	files := s.memoryFiles()
	out := model.MemoryResult{}
	var err error
	switch r.PathValue("action") {
	case "init":
		err = files.Ensure(id)
	case "list", "search":
		out.Documents, err = files.Documents(id, in.Query)
	case "read":
		var doc model.MemoryDocument
		doc, err = files.Read(id, in.Path)
		if err == nil {
			out.Documents = []model.MemoryDocument{doc}
		}
	case "write":
		var doc model.MemoryDocument
		doc, err = files.Write(id, in.Path, in.Content, in.BaseHash, "api")
		if err == nil {
			out.Documents = []model.MemoryDocument{doc}
		}
	case "history":
		out.History, err = files.History(id, in.Path)
	case "rollback":
		var doc model.MemoryDocument
		doc, err = files.Rollback(id, in.Path, in.Revision, in.BaseHash)
		if err == nil {
			out.Documents = []model.MemoryDocument{doc}
		}
	case "prepare", "context":
		if r.PathValue("action") == "context" && in.RunID != "" {
			if err = s.validateMemoryRun(r, id, in.RunID); err != nil {
				respondMemory(w, nil, err)
				return
			}
		}
		out.Context, err = files.Context(id, in.RunID, in.DryRun)
	case "updates":
		if in.RunID != "" {
			if err = s.validateMemoryRun(r, id, in.RunID); err != nil {
				respondMemory(w, nil, err)
				return
			}
		}
		var status model.MemoryStatus
		status, err = files.Status(id)
		if err == nil {
			changed := status.RunID != in.RunID
			for p, h := range status.Current {
				if status.Provided[p] != h {
					changed = true
				}
			}
			for p := range status.Provided {
				if _, ok := status.Current[p]; !ok {
					changed = true
				}
			}
			if changed {
				out.Context, err = files.Context(id, in.RunID, false)
			}
		}
	case "ack":
		if err = s.validateMemoryRun(r, id, in.RunID); err == nil {
			err = files.Acknowledge(id, in.RunID, in.Hashes)
		}
	case "status":
		var status model.MemoryStatus
		status, err = files.Status(id)
		out.Status = &status
	case "reflect-config", "reflect", "reflect-status", "reflect-apply", "reflect-discard":
		out, err = s.reflectionOperation(r.Context(), id, r.PathValue("action"), in)
	default:
		writeError(w, http.StatusNotFound, "Unknown memory operation")
		return
	}
	respondMemory(w, out, err)
}
func (s *Server) validateMemoryRun(r *http.Request, id model.MemoryIdentity, run string) error {
	current, err := s.store.GetAgent(r.Context(), id.Team, id.Name)
	if err != nil {
		return err
	}
	if current.RunID != run {
		return memory.ErrConflict
	}
	return nil
}

func (s *Server) lockPersonal(id model.MemoryIdentity) func() {
	s.memory.gateMu.Lock()
	if s.memory.gates == nil {
		s.memory.gates = map[model.MemoryIdentity]*sync.Mutex{}
	}
	gate := s.memory.gates[id]
	if gate == nil {
		gate = &sync.Mutex{}
		s.memory.gates[id] = gate
	}
	s.memory.gateMu.Unlock()
	gate.Lock()
	return gate.Unlock
}
