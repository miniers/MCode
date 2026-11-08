package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"mcode/internal/agent/llm"
	"mcode/internal/apisource"
	"mcode/internal/config"
	"mcode/internal/frontdesk"
	"mcode/internal/kanban"
	"mcode/internal/remote"
	"mcode/internal/store"
	"mcode/internal/textgen"
)

func ensureFrontdeskProject(ctx context.Context, backend *remote.MemoryBackend, dir string) (string, error) {
	if backend == nil {
		return "", errors.New("会话后端未接入")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if err = os.MkdirAll(abs, 0755); err != nil {
		return "", err
	}
	projects, err := backend.ListProjects(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range projects.Projects {
		if p.Path != "" && filepath.Clean(p.Path) == abs {
			return p.ID, nil
		}
	}
	p, err := backend.AddProjectPath(ctx, abs)
	if err != nil {
		return "", err
	}
	if p.Name != "总管" {
		p.Name = "总管"
		backend.AddProject(p)
	}
	return p.ID, nil
}

type frontdeskClient struct{ dataDir string }

func (c frontdeskClient) Stream(ctx context.Context, req llm.Request, delta func(llm.Delta)) (*llm.Response, error) {
	cfg, err := config.Load(c.dataDir)
	if err != nil {
		return nil, err
	}
	secrets := config.NewSecretStore("mcode")
	src, model, err := frontdesk.SelectSource(apisource.Resolve(cfg, secrets), secrets, cfg.Orchestration.Steward.Provider, cfg.Orchestration.Steward.Model)
	if err != nil {
		return nil, err
	}
	if src.DefaultModel == "" {
		src.DefaultModel = model
	}
	client, _, err := frontdesk.ClientFor(src, secrets)
	if err != nil {
		return nil, err
	}
	req.Model = model
	return client.Stream(ctx, req, delta)
}

type frontdeskReceiver struct {
	store   *store.Store
	dataDir string
	desk    *frontdesk.Desk
}

func (r frontdeskReceiver) IsFeed(id string) bool {
	feedID, _, err := r.store.GetSetting(context.Background(), "steward.feed_thread_id")
	if err != nil || id == "" || id != feedID {
		return false
	}
	cfg, err := config.Load(r.dataDir)
	return err == nil && cfg.Orchestration.Steward.Enabled
}
func (r frontdeskReceiver) Enqueue(id, text string, images []string) {
	var parts []llm.Part
	for _, path := range images {
		data, err := os.ReadFile(path)
		if err != nil {
			log.Printf("前台读取图片失败: %v", err)
			continue
		}
		mime := http.DetectContentType(data)
		if len(mime) >= 6 && mime[:6] == "image/" {
			parts = append(parts, llm.Part{Type: "image", MimeType: mime, Data: base64.StdEncoding.EncodeToString(data)})
		}
	}
	r.desk.Enqueue(frontdesk.Inbound{ThreadID: id, Text: text, Images: parts})
}

func (a *App) notifyFrontdesk(dataDir, title, body, thread string) error {
	if cfg, err := config.Load(dataDir); err == nil {
		if quiet, _ := cfg.Orchestration.Steward.QuietWindow(time.Now()); quiet {
			return nil
		}
	}
	a.notifyMu.RLock()
	fn := a.ownerNotify
	a.notifyMu.RUnlock()
	var err error
	if fn != nil {
		err = fn(title, body, thread)
	}
	if a.remote != nil && a.remote.SendOwnerPush(title, body, thread) {
		return nil
	}
	if fn == nil {
		return errors.New("桌面通知未接入")
	}
	return err
}

func wireFrontdesk(a *App, dataDir string) {
	if a == nil || a.store == nil || a.backend == nil || a.kanban == nil {
		return
	}
	ctx := context.Background()
	if err := migrateFrontdeskArchive(ctx, a.store, a.backend); err != nil {
		log.Printf("前台归档旧会话失败: %v", err)
	}
	cfg, err := config.Load(dataDir)
	if err != nil {
		log.Printf("前台读取配置失败: %v", err)
		cfg = config.Defaults()
	}
	home := cfg.Orchestration.Steward.ResolvedHomeDir(dataDir)
	var projectID string
	if cfg.Orchestration.Steward.Enabled {
		projectID, err = ensureFrontdeskProject(ctx, a.backend, home)
		if err != nil {
			log.Printf("前台创建项目失败: %v", err)
		}
		if projectID != "" {
			if _, err := a.backend.EnsureStewardFeedThread(ctx, projectID); err != nil {
				log.Printf("前台创建会话失败: %v", err)
			}
		}
	}
	services := &frontdeskServices{store: a.store, backend: a.backend, kanban: a.kanban, homeID: projectID, homePath: home}
	desk := &frontdesk.Desk{Client: frontdeskClient{dataDir: dataDir}, Feed: services, Tasks: services, Decisions: services, Memory: newFrontdeskMemory(a.store, dataDir), Projects: services, ProjectPath: home, CLIFallback: cfg.Orchestration.Steward.CLIFallback, Generator: textgen.GenerateWithOptions, CLIOptions: textgen.Options{Provider: cfg.Orchestration.Steward.Provider}}
	a.backend.SetStewardDesk(frontdeskReceiver{store: a.store, dataDir: dataDir, desk: desk})
	a.kanban.SetOnResult(frontdeskResultHandler(a, dataDir))
}

func frontdeskResultHandler(a *App, dataDir string) func(context.Context, store.Task, kanban.ExecutorResult) {
	return func(ctx context.Context, task store.Task, result kanban.ExecutorResult) {
		cfg, err := config.Load(dataDir)
		if err != nil || !cfg.Orchestration.Steward.Enabled {
			return
		}
		meta, err := kanban.ParseTaskMeta(task.Meta)
		if err != nil {
			log.Printf("前台读取任务元数据失败: %v", err)
			return
		}
		owner := meta.Origin == "owner" && meta.ParentID == ""
		kind, text, push := "", "", false
		switch result.Status {
		case kanban.ResultDone:
			if meta.NoChange {
				return
			}
			kind = "steward_result"
			text = fmt.Sprintf("%s：%s", task.Title, result.Summary)
			push = owner
		case kanban.ResultFailed:
			if !owner {
				log.Printf("任务 %s 失败: %s", task.ID, result.Summary)
				return
			}
			kind = "steward_result"
			text = fmt.Sprintf("%s 没能完成：%s", task.Title, result.Summary)
			push = true
		case kanban.ResultNeedsDecision:
			decision, err := a.store.GetStewardDecision(ctx, meta.DecisionID)
			if err != nil {
				log.Printf("前台读取决策失败: %v", err)
				return
			}
			var options json.RawMessage = []byte(decision.OptionsJSON)
			payload, err := json.Marshal(map[string]any{"id": decision.ID, "task": task.Title, "question": decision.Question, "options": options, "recommended": decision.Recommended})
			if err != nil {
				log.Printf("前台编码决策失败: %v", err)
				return
			}
			kind = "steward_decision"
			text = string(payload)
			push = true
		default:
			return
		}
		feedID, _, err := a.store.GetSetting(ctx, "steward.feed_thread_id")
		if err != nil || feedID == "" {
			log.Printf("前台读取会话失败: %v", err)
			return
		}
		if err := a.backend.AppendStewardMessage(ctx, feedID, "assistant", kind, text); err != nil {
			log.Printf("前台写入结果失败: %v", err)
			return
		}
		if push {
			body := text
			if result.Status == kanban.ResultNeedsDecision {
				body = result.Question
			}
			if err := a.notifyFrontdesk(dataDir, task.Title, body, feedID); err != nil {
				log.Printf("前台推送失败: %v", err)
			}
		}
	}
}
