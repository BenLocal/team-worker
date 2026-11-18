package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
)

func (a *app) chatCmd() *cobra.Command {
	chat := &cobra.Command{Use: "chat", Short: "Configure chat integrations"}
	feishu := &cobra.Command{Use: "feishu", Short: "Configure the built-in Feishu integration"}
	feishu.AddCommand(&cobra.Command{Use: "setup", Short: "Save Feishu credentials on the team-worker server", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.remote != "" {
				return errors.New("Run this command on the team-worker server without --remote")
			}
			tty, ok := a.in.(*os.File)
			if !ok || !term.IsTerminal(int(tty.Fd())) {
				return errors.New("An interactive terminal is required to enter the Feishu App Secret")
			}
			if _, err := fmt.Fprint(a.errOut, "Feishu App ID: "); err != nil {
				return err
			}
			appID, err := bufio.NewReader(tty).ReadString('\n')
			if err != nil {
				return err
			}
			if _, err := fmt.Fprint(a.errOut, "Feishu App Secret: "); err != nil {
				return err
			}
			secret, err := term.ReadPassword(int(tty.Fd()))
			if _, printErr := fmt.Fprintln(a.errOut); printErr != nil {
				return errors.Join(err, printErr)
			}
			if err != nil {
				return err
			}
			if err := config.SaveFeishuApp(config.DataDir(), config.FeishuApp{AppID: strings.TrimSpace(appID), AppSecret: string(secret)}); err != nil {
				return err
			}
			_, err = fmt.Fprintln(a.out, "Credentials saved. Check Settings → Feishu integration for connection status.")
			return err
		}})
	chat.AddCommand(feishu)
	chat.AddCommand(a.chatReplyCmd())
	return chat
}

func (a *app) chatReplyCmd() *cobra.Command {
	var progress, groupApproved bool
	var previews, images, files, docs []string
	cmd := &cobra.Command{Use: "reply TEAM REQUEST [MATERIAL]",
		Short: "Submit a Feishu reply for the routing agent to summarize",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			identity := config.IdentityFromEnv()
			runID := os.Getenv("TEAM_WORKER_RUN_ID")
			if a.remote != "" || identity.TeamName != args[0] || identity.AgentID == "" || runID == "" {
				return a.fail(errors.New("Run this command from the current Leader's local session"))
			}
			var material string
			if len(args) == 3 {
				material = args[2]
			} else {
				body, err := io.ReadAll(io.LimitReader(a.in, 16001))
				if err != nil {
					return a.fail(err)
				}
				material = string(body)
			}
			assets, err := replyAssets(previews, images, files, docs)
			if err != nil {
				return a.fail(err)
			}
			err = a.client().SubmitChatResult(cmd.Context(), args[0], args[1], model.ChatResultInput{
				AgentID: identity.AgentID, RunID: runID, Material: material, Final: !progress, GroupApproved: groupApproved,
				Assets: assets,
			})
			if err != nil {
				return a.fail(err)
			}
			a.emit(map[string]bool{"queued": true}, func(w io.Writer) { fmt.Fprintln(w, "Reply queued for summarization") })
			return nil
		},
	}
	cmd.Flags().BoolVar(&progress, "progress", false, "Submit a progress update instead of a final result")
	cmd.Flags().BoolVar(&groupApproved, "group-approved", false, "Confirm the material can be shared in the originating Feishu group")
	cmd.Flags().StringArrayVar(&previews, "image", nil, "Local image to preview in the Feishu card (repeatable)")
	cmd.Flags().StringArrayVar(&images, "image-reply", nil, "Local image to send as a separate quoted reply (repeatable)")
	cmd.Flags().StringArrayVar(&files, "file", nil, "Local file to send as a downloadable quoted reply (repeatable)")
	cmd.Flags().StringArrayVar(&docs, "doc", nil, "Cloud document link as TITLE=HTTPS_URL (repeatable)")
	return cmd
}

func replyAssets(previews, images, files, docs []string) ([]model.ChatAsset, error) {
	if len(previews)+len(images)+len(files)+len(docs) > model.ChatAssetCount {
		return nil, fmt.Errorf("Include at most %d images, files and links per result", model.ChatAssetCount)
	}
	var assets []model.ChatAsset
	for _, doc := range docs {
		title, link, ok := strings.Cut(doc, "=")
		if !ok {
			return nil, errors.New("Specify a cloud document as TITLE=HTTPS_URL")
		}
		assets = append(assets, model.ChatAsset{Kind: model.ChatAssetLink, Name: strings.TrimSpace(title), URL: strings.TrimSpace(link)})
	}
	remaining := model.ChatFileLimit
	for _, group := range []struct {
		kind  string
		paths []string
		limit int
	}{
		{model.ChatAssetPreview, previews, model.ChatImageLimit},
		{model.ChatAssetImage, images, model.ChatImageLimit},
		{model.ChatAssetFile, files, model.ChatFileLimit},
	} {
		for _, path := range group.paths {
			data, err := readReplyFile(path, min(group.limit, remaining))
			if err != nil {
				return nil, err
			}
			remaining -= len(data)
			assets = append(assets, model.ChatAsset{Kind: group.kind, Name: filepath.Base(path), Data: data})
		}
	}
	return assets, nil
}

func readReplyFile(path string, limit int) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > int64(limit) {
		return nil, fmt.Errorf("Attachment %q must be a nonempty regular file of at most %d bytes", path, limit)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err := errors.Join(err, file.Close()); err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > limit {
		return nil, fmt.Errorf("Attachment %q must be between 1 and %d bytes", path, limit)
	}
	return data, nil
}
