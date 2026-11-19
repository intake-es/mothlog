package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

type slack struct {
	url    string
	base   string
	client *http.Client
}

var slackEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func slackText(p *pending, link string) string {
	var b strings.Builder
	what := "Error nuevo"
	if p.kind == "regressed" {
		what = "Regresión"
	}
	fmt.Fprintf(&b, "*%s* en *%s*: ", what, slackEscaper.Replace(p.project))
	title := slackEscaper.Replace(oneLine(p.title))
	if link != "" {
		fmt.Fprintf(&b, "<%s|%s>\n", link, strings.ReplaceAll(title, "|", "¦"))
	} else {
		b.WriteString(title + "\n")
	}
	if p.culprit != "" {
		fmt.Fprintf(&b, "Culprit: `%s`\n", slackEscaper.Replace(oneLine(p.culprit)))
	}
	fmt.Fprintf(&b, "Release: %s · Entorno: %s · Eventos: %d",
		slackEscaper.Replace(orNone(p.release, "ninguno")), slackEscaper.Replace(orNone(p.environment, "ninguno")), p.count)
	return b.String()
}

func (s *slack) send(ctx context.Context, _ pgx.Tx, p *pending) error {
	body, err := json.Marshal(map[string]any{"text": slackText(p, issueLink(s.base, p.issueID))})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return cleanErr(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("slack: %w", cleanErr(err))
	}
	defer func() { _ = resp.Body.Close() }()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("slack: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

func oneLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(s)
}
