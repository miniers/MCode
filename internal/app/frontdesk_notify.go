package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mcode/internal/config"
)

// ErrOwnerNotifySuppressed 表示桌面通知被前台或安静时段抑制。
var ErrOwnerNotifySuppressed = errors.New("桌面通知被抑制（应用在前台或处于安静时段）")

// SetOwnerNotifier 设置向用户发桌面通知的实现。
func (a *App) SetOwnerNotifier(fn func(title, body, threadID string) error) {
	a.notifyMu.Lock()
	defer a.notifyMu.Unlock()
	a.ownerNotify = fn
}

// notifyAttention 同时通知已订阅的网页设备、桌面端与可选的 Webhook；桌面端自身负责前台和安静时段规则。
func (a *App) notifyAttention(title, body, threadID string) {
	if a.remote != nil {
		a.remote.SendOwnerPush(title, body, threadID)
	}
	a.notifyMu.RLock()
	fn := a.ownerNotify
	a.notifyMu.RUnlock()
	if fn != nil {
		_ = fn(title, body, threadID)
	}
	if dataDir := a.dataDir; dataDir != "" {
		go func() {
			cfg, err := config.Load(dataDir)
			if err != nil {
				log.Print("通知 webhook 读取配置失败: ", err)
				return
			}
			rawURL := strings.TrimSpace(cfg.Notifications.WebhookURL)
			if rawURL == "" {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client := &http.Client{Timeout: 5 * time.Second}
			if err := postWebhook(ctx, client, rawURL, title, body); err != nil {
				log.Print("通知 webhook 推送失败: ", err)
			}
		}()
	}
}

// webhookPayload 根据目标主机名构造对应格式的 Webhook JSON 请求体。
func webhookPayload(rawURL, title, body string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("无效的 webhook URL: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("不支持的 webhook URL scheme: %q", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return nil, errors.New("webhook URL 缺少主机名")
	}

	text := title + "\n" + body
	switch host {
	case "open.feishu.cn", "open.larksuite.com":
		var p struct {
			MsgType string `json:"msg_type"`
			Content struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		p.MsgType = "text"
		p.Content.Text = text
		return json.Marshal(p)
	case "oapi.dingtalk.com":
		var p struct {
			MsgType string `json:"msgtype"`
			Text    struct {
				Content string `json:"content"`
			} `json:"text"`
		}
		p.MsgType = "text"
		p.Text.Content = text
		return json.Marshal(p)
	default:
		var p struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		}
		p.Title = title
		p.Body = body
		return json.Marshal(p)
	}
}

// postWebhook 发送一条通知 Webhook POST 请求；错误信息不回显含密钥的完整 URL。
func postWebhook(ctx context.Context, client *http.Client, rawURL, title, body string) error {
	payload, err := webhookPayload(rawURL, title, body)
	if err != nil {
		return err
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(payload))
	if err != nil {
		return sanitizeWebhookError(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return sanitizeWebhookError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook 返回状态码 %d", resp.StatusCode)
	}
	return nil
}

// sanitizeWebhookError 剥离 *url.Error 中可能包含密钥的完整 URL。
func sanitizeWebhookError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("webhook %s 请求失败: %w", urlErr.Op, urlErr.Err)
	}
	return err
}
