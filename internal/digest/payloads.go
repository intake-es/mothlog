package digest

import (
	"strings"
	"time"
	"unicode/utf8"
)

// Límites de Block Kit.
const (
	maxBlocks      = 50
	maxSectionText = 3000
	maxHeaderText  = 150
	maxFields      = 10
	maxFieldText   = 2000
)

type SlackText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type SlackBlock struct {
	Type     string      `json:"type"`
	Text     *SlackText  `json:"text,omitempty"`
	Fields   []SlackText `json:"fields,omitempty"`
	Elements []any       `json:"elements,omitempty"`
}

type SlackButton struct {
	Type  string    `json:"type"`
	Text  SlackText `json:"text"`
	URL   string    `json:"url"`
	Style string    `json:"style,omitempty"`
}

type SlackMessage struct {
	Text   string       `json:"text"`
	Blocks []SlackBlock `json:"blocks"`
}

var slackEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func slackEsc(s string) string { return slackEscaper.Replace(s) }

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func mrkdwn(s string, limit int) SlackText {
	return SlackText{Type: "mrkdwn", Text: truncate(s, limit)}
}

// Slack es el resumen con Block Kit: una cabecera, el periodo y un bloque por sección.
func (d *Digest) Slack() SlackMessage {
	msg := SlackMessage{Text: slackEsc(d.Subject())}
	msg.Blocks = append(msg.Blocks,
		SlackBlock{Type: "header", Text: &SlackText{Type: "plain_text", Text: truncate(d.Title, maxHeaderText)}},
		SlackBlock{Type: "context", Elements: []any{mrkdwn(slackEsc(d.Period), maxSectionText)}})
	if d.Empty {
		t := mrkdwn(slackEsc(d.lang.t("empty")), maxSectionText)
		msg.Blocks = append(msg.Blocks, SlackBlock{Type: "section", Text: &t})
	}
	for _, s := range d.Sections {
		head := "*" + slackEsc(s.Title) + "*"
		if s.URL != "" {
			head = "*<" + s.URL + "|" + slackEsc(strings.ReplaceAll(s.Title, "|", "∣")) + ">*"
		}
		if s.Note != "" {
			head += "\n" + slackEsc(s.Note)
		}
		var lines []string
		for _, it := range s.Items {
			title := slackEsc(strings.ReplaceAll(oneLine(it.Title), "|", "∣"))
			if it.URL != "" {
				title = "<" + it.URL + "|" + title + ">"
			}
			line := "• " + title
			if it.Detail != "" {
				line += "\n   " + slackEsc(oneLine(it.Detail))
			}
			lines = append(lines, line)
		}
		blocks := []SlackBlock{{Type: "divider"}}
		text := head
		if len(lines) > 0 {
			text += "\n" + strings.Join(lines, "\n")
		}
		t := mrkdwn(text, maxSectionText)
		blocks = append(blocks, SlackBlock{Type: "section", Text: &t})
		if len(s.Stats) > 0 {
			var fields []SlackText
			for _, st := range s.Stats[:min(len(s.Stats), maxFields)] {
				f := "*" + slackEsc(st.Label) + "*\n" + slackEsc(st.Value)
				if st.Detail != "" {
					f += "  _" + slackEsc(st.Detail) + "_"
				}
				fields = append(fields, mrkdwn(f, maxFieldText))
			}
			blocks = append(blocks, SlackBlock{Type: "section", Fields: fields})
		}
		if len(msg.Blocks)+len(blocks) > maxBlocks-1 {
			break
		}
		msg.Blocks = append(msg.Blocks, blocks...)
	}
	if d.URL != "" {
		msg.Blocks = append(msg.Blocks, SlackBlock{Type: "actions", Elements: []any{
			SlackButton{Type: "button", Text: SlackText{Type: "plain_text", Text: d.lang.t("open")}, URL: d.URL},
		}})
	}
	return msg
}

// WebhookPayload es el cuerpo del webhook de un resumen.
type WebhookPayload struct {
	// Type es siempre digest.
	Type string `json:"type"`
	// Delivery es el mismo en los reintentos del mismo resumen.
	Delivery  string         `json:"delivery"`
	SentAt    time.Time      `json:"sent_at"`
	Project   WebhookProject `json:"project"`
	Frequency string         `json:"frequency"`
	Language  string         `json:"language"`
	Period    WebhookPeriod  `json:"period"`
	Title     string         `json:"title"`
	Empty     bool           `json:"empty"`
	Sections  []Section      `json:"sections"`
	URL       string         `json:"url,omitempty"`
	Markdown  string         `json:"markdown"`
}

type WebhookProject struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type WebhookPeriod struct {
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Timezone string    `json:"timezone"`
}

// Webhook es el cuerpo del webhook: el resumen por secciones y en Markdown.
func (d *Digest) Webhook(delivery string, sentAt time.Time) WebhookPayload {
	return WebhookPayload{
		Type: "digest", Delivery: delivery, SentAt: sentAt.UTC(),
		Project: WebhookProject{ID: d.ProjectID, Name: d.Project}, Frequency: d.Frequency, Language: d.Language,
		Period: WebhookPeriod{Start: d.Start, End: d.End, Timezone: d.Timezone},
		Title:  d.Title, Empty: d.Empty, Sections: d.Sections, URL: d.URL, Markdown: d.Markdown(),
	}
}
