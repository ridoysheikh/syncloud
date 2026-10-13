package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"
)

// ChannelConfig holds every channel type's settings; only the type's
// fields are used. URLs and tokens are secrets: they are sealed at rest and
// never returned by the API.
type ChannelConfig struct {
	// webhook, slack, discord
	URL string `json:"url,omitempty"`
	// telegram
	BotToken string `json:"botToken,omitempty"`
	ChatID   string `json:"chatId,omitempty"`
	// email
	SMTPHost string   `json:"smtpHost,omitempty"`
	SMTPPort int      `json:"smtpPort,omitempty"`
	Username string   `json:"username,omitempty"`
	Password string   `json:"password,omitempty"`
	From     string   `json:"from,omitempty"`
	To       []string `json:"to,omitempty"`
}

// ChannelTypes lists the supported channels.
var ChannelTypes = []string{"webhook", "slack", "discord", "telegram", "email"}

// TelegramAPI is the Bot API base URL (a variable for tests).
var TelegramAPI = "https://api.telegram.org"

func validURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return errors.New("url must be an http(s) URL")
	}
	return nil
}

// Validate checks a channel's settings and returns a summary safe to show.
func (c *ChannelConfig) Validate(typ string) (string, error) {
	switch typ {
	case "webhook", "slack", "discord":
		if err := validURL(c.URL); err != nil {
			return "", err
		}
		u, _ := url.Parse(c.URL)
		return u.Host, nil
	case "telegram":
		if c.BotToken == "" || c.ChatID == "" {
			return "", errors.New("telegram needs botToken and chatId")
		}
		return "chat " + c.ChatID, nil
	case "email":
		// Recipients only: the platform SMTP (Settings → Email) sends. A
		// channel may still carry its own server (smtpHost and from).
		if len(c.To) == 0 {
			return "", errors.New("email needs at least one address in to")
		}
		addrs := c.To
		if c.SMTPHost != "" {
			if c.From == "" {
				return "", errors.New("an email channel with its own smtpHost needs from")
			}
			if c.SMTPPort == 0 {
				c.SMTPPort = 587
			}
			addrs = append([]string{c.From}, c.To...)
		}
		for _, a := range addrs {
			if _, err := mail.ParseAddress(a); err != nil {
				return "", fmt.Errorf("invalid address %q", a)
			}
		}
		return strings.Join(c.To, ", "), nil
	}
	return "", fmt.Errorf("type must be one of %s", strings.Join(ChannelTypes, ", "))
}

// Notification is what a channel delivers.
type Notification struct {
	Kind     string    `json:"status"` // firing | reminder | resolved | event | test
	Rule     string    `json:"rule"`
	Severity string    `json:"severity"`
	Instance string    `json:"instance"` // e.g. shop/production/web or node w2
	Message  string    `json:"message"`
	Value    *float64  `json:"value,omitempty"`
	At       time.Time `json:"at"`
	URL      string    `json:"url,omitempty"` // the dashboard
}

// Title is a one-line summary.
func (n Notification) Title() string {
	prefix := map[string]string{"firing": "FIRING", "resolved": "RESOLVED", "event": "ALERT", "test": "TEST", "reminder": "STILL FIRING"}[n.Kind]
	return fmt.Sprintf("[%s] %s: %s", prefix, n.Rule, n.Instance)
}

// Text is the plain-text body used by chat channels and email.
func (n Notification) Text() string {
	var b strings.Builder
	b.WriteString(n.Title())
	if n.Severity != "" && n.Kind != "resolved" {
		b.WriteString(" (" + n.Severity + ")")
	}
	b.WriteString("\n" + n.Message)
	if n.URL != "" {
		b.WriteString("\n" + n.URL)
	}
	return b.String()
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

func postJSON(ctx context.Context, target string, body any) error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // chat text, not HTML: keep ">" and "&" as they are
	if err := enc.Encode(body); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, &b)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "SynCloud-Alerts")
	resp, err := httpClient.Do(req)
	if err != nil {
		// The URL is a secret (Slack and Discord webhooks, the Telegram bot
		// token): keep it out of errors, which are stored and shown.
		var ue *url.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("request to %s failed: %w", req.URL.Host, ue.Err)
		}
		return errors.New("request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// Send delivers n through one channel.
func Send(ctx context.Context, typ string, c ChannelConfig, n Notification) error {
	switch typ {
	case "webhook":
		return postJSON(ctx, c.URL, n)
	case "slack":
		return postJSON(ctx, c.URL, map[string]string{"text": n.Text()})
	case "discord":
		return postJSON(ctx, c.URL, map[string]string{"content": n.Text()})
	case "telegram":
		return postJSON(ctx, TelegramAPI+"/bot"+c.BotToken+"/sendMessage", map[string]any{"chat_id": c.ChatID, "text": n.Text(), "disable_web_page_preview": true})
	case "email":
		// Without a manager there is no platform SMTP: only a channel's own server.
		if c.SMTPHost == "" {
			return ErrNoSMTP
		}
		msg, err := buildMail(mailParts{From: c.From, FromName: "SynCloud", To: c.To, Subject: n.Title(), Text: n.Text(),
			HTML: renderHTML("SynCloud", false, n), At: n.At})
		if err != nil {
			return err
		}
		return deliver(ctx, channelSMTP(c), c.To, msg)
	}
	return fmt.Errorf("unknown channel type %q", typ)
}
