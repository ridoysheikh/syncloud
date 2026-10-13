package alerts

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// SMTP is the platform's mail server (Phase 20a). Email channels use it
// unless they carry their own server.
type SMTP struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Security string `json:"security"` // starttls | tls | none
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	From     string `json:"from"`
	FromName string `json:"fromName,omitempty"` // default: the workspace name
}

// SMTPView is the SMTP configuration as the API shows it (no password).
type SMTPView struct {
	Configured  bool   `json:"configured"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Security    string `json:"security"`
	Username    string `json:"username"`
	HasPassword bool   `json:"hasPassword"`
	From        string `json:"from"`
	FromName    string `json:"fromName"`
}

// ErrNoSMTP means email can't be sent until SMTP is set up.
var ErrNoSMTP = errors.New("email is not set up: add an SMTP server under Settings → Email")

const (
	settingSMTP = "smtp.config"
	smtpAAD     = "smtp-config"
)

// Validate checks the settings and fills in defaults.
func (c *SMTP) Validate() error {
	c.Host = strings.TrimSpace(c.Host)
	c.From = strings.TrimSpace(c.From)
	c.FromName = strings.TrimSpace(c.FromName)
	if c.Host == "" || strings.ContainsAny(c.Host, " /:") {
		return invalid("host is required (a name or address, without a port)")
	}
	if c.Security == "" {
		c.Security = "starttls"
	}
	switch c.Security {
	case "starttls", "tls", "none":
	default:
		return invalid("security must be starttls, tls or none")
	}
	if c.Port == 0 {
		c.Port = map[string]int{"starttls": 587, "tls": 465, "none": 25}[c.Security]
	}
	if c.Port < 1 || c.Port > 65535 {
		return invalid("port must be 1–65535")
	}
	if a, err := mail.ParseAddress(c.From); err != nil || a.Address != c.From {
		return invalid("from must be an email address like alerts@example.com")
	}
	if strings.ContainsAny(c.FromName, "\r\n<>\"") || len(c.FromName) > 60 {
		return invalid("from name may not contain quotes, < or > (at most 60 characters)")
	}
	if c.Password != "" && c.Username == "" {
		return invalid("a password needs a username")
	}
	return nil
}

func (m *Manager) smtpConfig(ctx context.Context) (*SMTP, error) {
	v, ok, err := m.st.GetSetting(ctx, settingSMTP)
	if err != nil || !ok {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil, err
	}
	b, err := m.box.Open(raw, []byte(smtpAAD))
	if err != nil {
		return nil, err
	}
	var c SMTP
	return &c, json.Unmarshal(b, &c)
}

// SMTP returns the platform SMTP settings without the password.
func (m *Manager) SMTP(ctx context.Context) (SMTPView, error) {
	c, err := m.smtpConfig(ctx)
	if err != nil || c == nil {
		return SMTPView{Security: "starttls", Port: 587}, err
	}
	return SMTPView{Configured: true, Host: c.Host, Port: c.Port, Security: c.Security, Username: c.Username,
		HasPassword: c.Password != "", From: c.From, FromName: c.FromName}, nil
}

// SetSMTP saves the platform SMTP settings. An empty password keeps the
// stored one while the username is unchanged.
func (m *Manager) SetSMTP(ctx context.Context, in SMTP) (SMTPView, error) {
	if in.Password == "" && in.Username != "" {
		if cur, err := m.smtpConfig(ctx); err == nil && cur != nil && cur.Username == in.Username {
			in.Password = cur.Password
		}
	}
	if err := in.Validate(); err != nil {
		return SMTPView{}, err
	}
	b, _ := json.Marshal(in)
	if err := m.st.SetSetting(ctx, settingSMTP, base64.StdEncoding.EncodeToString(m.box.Seal(b, []byte(smtpAAD)))); err != nil {
		return SMTPView{}, err
	}
	return m.SMTP(ctx)
}

// DeleteSMTP turns email off (channels with their own server keep working).
func (m *Manager) DeleteSMTP(ctx context.Context) error {
	return m.st.DeleteSetting(ctx, settingSMTP)
}

// TestSMTP sends a test email to one address and returns the server's error.
func (m *Manager) TestSMTP(ctx context.Context, to string) error {
	if a, err := mail.ParseAddress(to); err != nil || a.Address != to {
		return invalid("give one email address to send the test to")
	}
	c, err := m.smtpConfig(ctx)
	if err != nil {
		return err
	}
	if c == nil {
		return ErrNoSMTP
	}
	name, _ := m.brand()
	n := Notification{Kind: "test", Rule: "Test email", Severity: "info", Instance: name,
		Message: "Email from " + name + " reaches this address. Alerts will arrive like this.", At: m.now().UTC(), URL: m.dashboardURL()}
	return m.mail(ctx, *c, []string{to}, n)
}

func (m *Manager) brand() (string, []byte) {
	if m.Brand == nil {
		return "SynCloud", nil
	}
	name, logo := m.Brand()
	if name == "" {
		name = "SynCloud"
	}
	return name, logo
}

// sendEmail delivers n to an email channel: through its own server when it
// has one (channels from before Phase 20), else the platform's.
func (m *Manager) sendEmail(ctx context.Context, cfg ChannelConfig, n Notification) error {
	if cfg.SMTPHost != "" {
		return m.mail(ctx, channelSMTP(cfg), cfg.To, n)
	}
	c, err := m.smtpConfig(ctx)
	if err != nil {
		return err
	}
	if c == nil {
		return ErrNoSMTP
	}
	return m.mail(ctx, *c, cfg.To, n)
}

// channelSMTP is a channel's own server: implicit TLS on 465, otherwise
// STARTTLS when the server offers it (as before Phase 20).
func channelSMTP(cfg ChannelConfig) SMTP {
	sec := "auto"
	if cfg.SMTPPort == 465 {
		sec = "tls"
	}
	return SMTP{Host: cfg.SMTPHost, Port: cfg.SMTPPort, Security: sec, Username: cfg.Username, Password: cfg.Password, From: cfg.From}
}

func (m *Manager) mail(ctx context.Context, c SMTP, to []string, n Notification) error {
	name, logo := m.brand()
	fromName := c.FromName
	if fromName == "" {
		fromName = name
	}
	msg, err := buildMail(mailParts{From: c.From, FromName: fromName, To: to, Subject: n.Title(), Text: n.Text(),
		HTML: renderHTML(name, logo != nil, n), Logo: logo, At: n.At})
	if err != nil {
		return err
	}
	return deliver(ctx, c, to, msg)
}

type mailParts struct {
	From, FromName string
	To             []string
	Subject        string
	Text, HTML     string
	Logo           []byte // PNG, shown inline as cid:logo
	At             time.Time
}

func boundary() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "sc-" + hex.EncodeToString(b)
}

func qp(s string) string {
	var b bytes.Buffer
	w := quotedprintable.NewWriter(&b)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return b.String()
}

// buildMail writes a MIME message: plain text and HTML alternatives, with
// the logo as an inline image when there is one.
func buildMail(p mailParts) ([]byte, error) {
	for _, h := range append([]string{p.From, p.FromName, p.Subject}, p.To...) {
		if strings.ContainsAny(h, "\r\n") {
			return nil, errors.New("header values may not contain line breaks")
		}
	}
	at := p.At
	if at.IsZero() {
		at = time.Now()
	}
	var b strings.Builder
	from := (&mail.Address{Name: p.FromName, Address: p.From}).String()
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + strings.Join(p.To, ", ") + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", p.Subject) + "\r\n")
	b.WriteString("Date: " + at.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	alt := boundary()
	altPart := func() {
		b.WriteString("--" + alt + "\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
		b.WriteString(qp(strings.ReplaceAll(p.Text, "\n", "\r\n")) + "\r\n")
		b.WriteString("--" + alt + "\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
		b.WriteString(qp(p.HTML) + "\r\n")
		b.WriteString("--" + alt + "--\r\n")
	}
	if len(p.Logo) == 0 {
		b.WriteString("Content-Type: multipart/alternative; boundary=\"" + alt + "\"\r\n\r\n")
		altPart()
		return []byte(b.String()), nil
	}
	rel := boundary()
	b.WriteString("Content-Type: multipart/related; boundary=\"" + rel + "\"\r\n\r\n")
	b.WriteString("--" + rel + "\r\nContent-Type: multipart/alternative; boundary=\"" + alt + "\"\r\n\r\n")
	altPart()
	b.WriteString("--" + rel + "\r\nContent-Type: image/png\r\nContent-Transfer-Encoding: base64\r\nContent-ID: <logo>\r\nContent-Disposition: inline; filename=\"logo.png\"\r\n\r\n")
	enc := base64.StdEncoding.EncodeToString(p.Logo)
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n--" + rel + "--\r\n")
	return []byte(b.String()), nil
}

var mailTmpl = template.Must(template.New("mail").Parse(`<!doctype html>
<html><body style="margin:0;padding:24px;background:#0f1115;font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;color:#e8eaed">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:560px;margin:0 auto;background:#171a21;border:1px solid #2a2f3a;border-radius:6px">
<tr><td style="padding:14px 18px;border-bottom:1px solid #2a2f3a">
{{if .Logo}}<img src="cid:logo" width="22" height="22" alt="" style="vertical-align:middle;border-radius:3px;margin-right:8px">{{end}}<span style="font-weight:600;font-size:15px;vertical-align:middle">{{.Brand}}</span>
</td></tr>
<tr><td style="padding:18px">
<div style="display:inline-block;padding:2px 8px;border-radius:3px;font-size:12px;font-weight:600;color:#0f1115;background:{{.Color}}">{{.Status}}</div>
<h1 style="font-size:17px;margin:10px 0 4px">{{.Rule}}</h1>
<div style="font-size:13px;color:#b7bcc6;margin-bottom:14px">{{.Instance}}</div>
<div style="font-size:14px;line-height:1.5;white-space:pre-wrap">{{.Message}}</div>
<table role="presentation" cellpadding="0" cellspacing="0" style="margin-top:14px;font-size:12px;color:#b7bcc6">
{{if .Severity}}<tr><td style="padding:2px 12px 2px 0">Severity</td><td>{{.Severity}}</td></tr>{{end}}
<tr><td style="padding:2px 12px 2px 0">When</td><td>{{.When}}</td></tr>
</table>
{{if .URL}}<a href="{{.URL}}" style="display:inline-block;margin-top:18px;padding:8px 14px;background:#e8eaed;color:#0f1115;text-decoration:none;border-radius:4px;font-size:13px;font-weight:600">Open {{.Brand}}</a>{{end}}
</td></tr></table>
<p style="max-width:560px;margin:10px auto 0;font-size:11px;color:#9aa0aa;text-align:center">Sent by {{.Brand}} alerts.</p>
</body></html>`))

func renderHTML(brand string, logo bool, n Notification) string {
	status := map[string]string{"firing": "FIRING", "resolved": "RESOLVED", "event": "ALERT", "test": "TEST", "reminder": "STILL FIRING"}[n.Kind]
	color := map[string]string{"critical": "#f28b82", "warning": "#fdd663", "info": "#8ab4f8"}[n.Severity]
	if n.Kind == "resolved" {
		color = "#81c995"
	}
	if color == "" {
		color = "#8ab4f8"
	}
	sev := n.Severity
	if n.Kind == "resolved" {
		sev = ""
	}
	var b strings.Builder
	_ = mailTmpl.Execute(&b, map[string]any{"Brand": brand, "Logo": logo, "Status": status, "Color": color, "Rule": n.Rule,
		"Instance": n.Instance, "Message": n.Message, "Severity": sev, "When": n.At.UTC().Format("2006-01-02 15:04 MST"), "URL": n.URL})
	return b.String()
}

// plainAuth sends AUTH PLAIN without net/smtp's TLS check; used only when
// security is "none" and the admin chose that.
type plainAuth struct{ user, pass string }

func (a plainAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pass), nil
}
func (a plainAuth) Next([]byte, bool) ([]byte, error) { return nil, nil }

// deliver sends msg through c: "tls" is implicit TLS, "starttls" requires
// STARTTLS, "auto" uses it when offered, "none" sends in the clear.
func deliver(ctx context.Context, c SMTP, to []string, msg []byte) error {
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var conn net.Conn
	var err error
	tlsCfg := &tls.Config{ServerName: c.Host}
	if c.Security == "tls" {
		conn, err = (&tls.Dialer{NetDialer: &net.Dialer{}, Config: tlsCfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		return err
	}
	defer cl.Close()
	if c.Security == "starttls" || c.Security == "auto" {
		if ok, _ := cl.Extension("STARTTLS"); ok {
			if err := cl.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("STARTTLS: %w", err)
			}
		} else if c.Security == "starttls" {
			return errors.New("the server does not offer STARTTLS; choose TLS (port 465) or none")
		}
	}
	if c.Username != "" {
		var a smtp.Auth = smtp.PlainAuth("", c.Username, c.Password, c.Host)
		if c.Security == "none" {
			a = plainAuth{c.Username, c.Password}
		}
		if err := cl.Auth(a); err != nil {
			return fmt.Errorf("sign-in: %w", err)
		}
	}
	if err := cl.Mail(c.From); err != nil {
		return err
	}
	for _, r := range to {
		if err := cl.Rcpt(r); err != nil {
			return fmt.Errorf("recipient %s: %w", r, err)
		}
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return cl.Quit()
}
