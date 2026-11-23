package alert

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/url"
	"strings"
	"time"

	"github.com/intake-es/intake-errors/internal/markdown"
	"github.com/intake-es/intake-errors/internal/store"
)

type smtpConfig struct {
	addr     string
	host     string
	implicit bool
	user     string
	password string
	from     string
}

func parseSMTP(raw, from string) (*smtpConfig, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "smtp" && u.Scheme != "smtps") || u.Hostname() == "" {
		return nil, errors.New("SMTP_URL debe ser smtp://usuario:clave@host:puerto o smtps://…")
	}
	if from == "" {
		return nil, errors.New("con SMTP_URL hace falta SMTP_FROM, el remitente")
	}
	if _, err := mail.ParseAddress(from); err != nil {
		return nil, fmt.Errorf("SMTP_FROM no es una dirección: %q", from)
	}
	c := &smtpConfig{host: u.Hostname(), implicit: u.Scheme == "smtps", from: from}
	port := u.Port()
	if port == "" {
		port = map[bool]string{true: "465", false: "587"}[c.implicit]
	}
	c.addr = net.JoinHostPort(c.host, port)
	if u.User != nil {
		c.user = u.User.Username()
		c.password, _ = u.User.Password()
	}
	return c, nil
}

type email struct {
	st  *store.Store
	cfg *smtpConfig
	to  []string
}

func (e *email) message(ctx context.Context, p *pending, now time.Time) ([]byte, error) {
	s := summarize(p)
	_, fallback := kindText(p)
	subject := "[intake-errors] " + fallback + p.project + ": " + s.Headline
	var body strings.Builder
	body.WriteString(s.text(p.link))
	if !p.isMonitor() {
		d, err := issueDetail(ctx, e.st, p)
		if err != nil {
			return nil, err
		}
		body.WriteString("\n---\n\n" + markdown.Issue(d))
	}

	var m strings.Builder
	header := func(k, v string) { m.WriteString(k + ": " + v + "\r\n") }
	header("From", e.cfg.from)
	header("To", strings.Join(e.to, ", "))
	header("Subject", mime.QEncoding.Encode("utf-8", oneLine(subject)))
	header("Date", now.Format(time.RFC1123Z))
	header("Message-ID", "<"+newUUID()+"@intake-errors>")
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "8bit")
	m.WriteString("\r\n")
	m.WriteString(strings.ReplaceAll(body.String(), "\n", "\r\n"))
	return []byte(m.String()), nil
}

func (e *email) send(ctx context.Context, p *pending) error {
	var msg []byte
	var err error
	if p.digest != nil {
		msg = e.digestMessage(p.digest, time.Now())
	} else if msg, err = e.message(ctx, p, time.Now()); err != nil {
		return err
	}
	if err := e.deliver(ctx, msg); err != nil {
		return fmt.Errorf("correo: %w", err)
	}
	return nil
}

func (e *email) deliver(ctx context.Context, msg []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", e.cfg.addr)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if e.cfg.implicit {
		conn = tls.Client(conn, &tls.Config{ServerName: e.cfg.host, MinVersion: tls.VersionTLS12})
	}
	c, err := smtp.NewClient(conn, e.cfg.host)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if !e.cfg.implicit {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: e.cfg.host, MinVersion: tls.VersionTLS12}); err != nil {
				return err
			}
		}
	}
	if e.cfg.user != "" {
		// PlainAuth se niega a mandar la clave sin TLS salvo a localhost.
		if err := c.Auth(smtp.PlainAuth("", e.cfg.user, e.cfg.password, e.cfg.host)); err != nil {
			return err
		}
	}
	from, err := mail.ParseAddress(e.cfg.from)
	if err != nil {
		return err
	}
	if err := c.Mail(from.Address); err != nil {
		return err
	}
	for _, to := range e.to {
		if err := c.Rcpt(to); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
