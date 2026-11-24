package alert

import (
	"context"
	"html"
	"mime"
	"net/http"
	"strings"
)

const TelegramEndpoint = "https://api.telegram.org"

// Límites de Discord y Telegram.
const (
	maxDiscordContent = 2000
	maxDiscordTitle   = 256
	maxDiscordDesc    = 4096
	maxTelegramText   = 4096
)

type discord struct {
	url    string
	client *http.Client
}

var discordEscaper = strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "~", `\~`, "`", "'", "|", `\|`, ">", `\>`, "@", "@\u200b",
	"[", `\[`, "]", `\]`, "(", `\(`, ")", `\)`)

type discordEmbed struct {
	Title       string         `json:"title"`
	URL         string         `json:"url,omitempty"`
	Description string         `json:"description,omitempty"`
	Fields      []discordField `json:"fields,omitempty"`
}

type discordField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

type discordPayload struct {
	Content         string              `json:"content"`
	Embeds          []discordEmbed      `json:"embeds"`
	AllowedMentions map[string][]string `json:"allowed_mentions"`
}

func discordMessage(p *pending) discordPayload {
	s := summarize(p)
	content := "**" + discordEscaper.Replace(s.What) + "** · " + discordEscaper.Replace(strings.Join(s.Meta, " · "))
	embed := discordEmbed{Title: truncate(s.Headline, maxDiscordTitle), URL: p.link}
	var desc strings.Builder
	if s.Location != "" {
		desc.WriteString("`" + strings.ReplaceAll(s.Location, "`", "'") + "`")
	}
	if s.Code != "" {
		desc.WriteString("\n```\n" + strings.ReplaceAll(s.Code, "```", "`\u200b``") + "\n```")
	}
	embed.Description = truncate(desc.String(), maxDiscordDesc)
	for _, f := range s.Fields {
		embed.Fields = append(embed.Fields, discordField{Name: f[0], Value: discordEscaper.Replace(f[1]), Inline: true})
	}
	return discordPayload{
		Content:         truncate(content, maxDiscordContent),
		Embeds:          []discordEmbed{embed},
		AllowedMentions: map[string][]string{"parse": {}},
	}
}

func (d *discord) send(ctx context.Context, p *pending) error {
	return postJSON(ctx, d.client, "discord", d.url, discordMessage(p), nil)
}

type telegram struct {
	api    string
	token  string
	chat   string
	client *http.Client
}

// telegramText es el aviso para Telegram y si va en HTML.
func telegramText(p *pending) (text string, isHTML bool) {
	s := summarize(p)
	e := html.EscapeString
	var b strings.Builder
	b.WriteString("<b>" + e(s.What) + "</b> · " + e(strings.Join(s.Meta, " · ")) + "\n")
	b.WriteString("<b>" + e(s.Headline) + "</b>\n")
	if s.Location != "" {
		b.WriteString("<code>" + e(s.Location) + "</code>\n")
	}
	if s.Code != "" {
		b.WriteString("<pre>" + e(s.Code) + "</pre>\n")
	}
	for _, f := range s.Fields {
		b.WriteString(e(f[0]) + ": " + e(f[1]) + "\n")
	}
	if p.link != "" {
		label := p.l.T("View error", "Ver el error")
		if p.isMonitor() {
			label = p.l.T("View monitor", "Ver el monitor")
		}
		b.WriteString(`<a href="` + e(p.link) + `">` + label + `</a>`)
	}
	text = b.String()
	if len([]rune(text)) > maxTelegramText {
		// Cortar el HTML podría dejar una etiqueta abierta: mejor sin formato.
		return truncate(s.text(p.link), maxTelegramText), false
	}
	return text, true
}

func (t *telegram) send(ctx context.Context, p *pending) error {
	text, isHTML := telegramText(p)
	payload := map[string]any{"chat_id": t.chat, "text": text, "link_preview_options": map[string]bool{"is_disabled": true}}
	if isHTML {
		payload["parse_mode"] = "HTML"
	}
	return postJSON(ctx, t.client, "telegram", t.api+"/bot"+t.token+"/sendMessage", payload, nil)
}

type ntfy struct {
	url    string
	token  string
	client *http.Client
}

var ntfyTags = map[string]string{
	"new": "rotating_light", "regressed": "repeat", "frequency": "chart_with_upwards_trend",
	"users": "busts_in_silhouette", kindTest: "test_tube",
	"cron_failed": "x", "cron_missed": "alarm_clock", "cron_timeout": "hourglass", "cron_recovered": "white_check_mark",
	"uptime_down": "red_circle", "uptime_up": "white_check_mark",
}

func (n *ntfy) send(ctx context.Context, p *pending) error {
	s := summarize(p)
	h := http.Header{}
	h.Set("Content-Type", "text/plain; charset=utf-8")
	// Las cabeceras van en ASCII: lo demás, codificado como en el correo.
	h.Set("Title", mime.QEncoding.Encode("utf-8", s.What+" · "+p.project))
	h.Set("Tags", ntfyTags[p.kind])
	if p.kind == "new" || p.kind == "regressed" || (isCronKind(p.kind) && p.kind != "cron_recovered") || p.kind == "uptime_down" {
		h.Set("Priority", "high")
	}
	if p.link != "" {
		h.Set("Click", p.link)
	}
	if n.token != "" {
		h.Set("Authorization", "Bearer "+n.token)
	}
	return post(ctx, n.client, "ntfy", n.url, []byte(s.text("")), h)
}
