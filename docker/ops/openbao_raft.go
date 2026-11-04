package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// openbaoRaftCmd prepares OpenBao's raft storage before the server starts.
func openbaoRaftCmd() *cobra.Command {
	var dataDir, nodeID, clusterAddr string
	cmd := &cobra.Command{
		Use:   "openbao-raft",
		Short: "Let raft data written under another node ID start as this node",
		Long: "Run before `bao server`. Raft records the node ID it was created under; data from a\n" +
			"deployment that used another one (ClawTeam's \"clawteam\") leaves this single node a\n" +
			"standby forever, so OpenBao never serves secrets. Once per data directory this writes\n" +
			"raft's peers.json recovery file naming only this node, which raft applies at start.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if os.Getenv("TEAM_WORKER_OPENBAO") != "1" {
				return nil
			}
			wrote, err := prepareRaft(dataDir, nodeID, clusterAddr)
			if err != nil {
				return failWith("OpenBao raft preparation failed: ", err)
			}
			if wrote {
				fmt.Printf("openbao: raft data is recovered as node %q at the next start.\n", nodeID)
			}
			return nil
		},
	}
	// The defaults match /etc/openbao/openbao.hcl.
	cmd.Flags().StringVar(&dataDir, "data", "/home/node/.openbao/data", "OpenBao raft storage path")
	cmd.Flags().StringVar(&nodeID, "node-id", "team-worker", "The node_id openbao.hcl gives raft")
	cmd.Flags().StringVar(&clusterAddr, "cluster-addr", "127.0.0.1:8201", "The node's cluster address, without scheme")
	return cmd
}

// prepareRaft writes raft/peers.json naming only this node, once, for raft
// data that exists but was not yet prepared for nodeID. New data needs
// nothing: raft starts under the configured ID. It reports whether it wrote
// the file.
func prepareRaft(dataDir, nodeID, clusterAddr string) (bool, error) {
	raftDir := filepath.Join(dataDir, "raft")
	if _, err := os.Stat(filepath.Join(raftDir, "raft.db")); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	marker := filepath.Join(raftDir, "team-worker-node-id")
	if data, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(data)) == nodeID {
		return false, nil
	}
	peers, err := json.Marshal([]map[string]any{{"id": nodeID, "address": clusterAddr, "non_voter": false}})
	if err != nil {
		return false, err
	}
	if err := writeAtomic(filepath.Join(raftDir, "peers.json"), peers, 0o600); err != nil {
		return false, err
	}
	return true, writeAtomic(marker, []byte(nodeID+"\n"), 0o600)
}
