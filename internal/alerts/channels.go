package alerts

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"strconv"
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
		if c.SMTPHost == "" || c.From == "" || len(c.To) == 0 {
			return "", errors.New("email needs smtpHost, from and at least one address in to")
		}
		if c.SMTPPort == 0 {
			c.SMTPPort = 587
		}
		for _, a := range append([]string{c.From}, c.To...) {
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
	Kind     string    `json:"status"` // firing | resolved | event | test
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
	prefix := map[string]string{"firing": "FIRING", "resolved": "RESOLVED", "event": "ALERT", "test": "TEST"}[n.Kind]
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
		return sendMail(ctx, c, n)
	}
	return fmt.Errorf("unknown channel type %q", typ)
}

func sendMail(ctx context.Context, c ChannelConfig, n Notification) error {
	addr := net.JoinHostPort(c.SMTPHost, strconv.Itoa(c.SMTPPort))
	msg := "From: " + c.From + "\r\nTo: " + strings.Join(c.To, ", ") + "\r\nSubject: " + n.Title() +
		"\r\nDate: " + n.At.Format(time.RFC1123Z) + "\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" +
		strings.ReplaceAll(n.Text(), "\n", "\r\n") + "\r\n"
	var auth smtp.Auth
	if c.Username != "" {
		auth = smtp.PlainAuth("", c.Username, c.Password, c.SMTPHost)
	}
	if c.SMTPPort != 465 {
		// 587/25: STARTTLS when the server offers it (net/smtp does that).
		done := make(chan error, 1)
		go func() { done <- smtp.SendMail(addr, auth, c.From, c.To, []byte(msg)) }()
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Second):
			return errors.New("SMTP timed out")
		}
	}
	// 465: implicit TLS.
	d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: &tls.Config{ServerName: c.SMTPHost}}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	cl, err := smtp.NewClient(conn, c.SMTPHost)
	if err != nil {
		return err
	}
	defer cl.Close()
	if auth != nil {
		if err := cl.Auth(auth); err != nil {
			return err
		}
	}
	if err := cl.Mail(c.From); err != nil {
		return err
	}
	for _, to := range c.To {
		if err := cl.Rcpt(to); err != nil {
			return err
		}
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return cl.Quit()
}
