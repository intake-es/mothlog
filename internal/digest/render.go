package digest

import (
	"fmt"
	"html"
	"strings"
	"unicode"
)

// Subject es el asunto del correo: el título y lo más importante del periodo.
func (d *Digest) Subject() string {
	l := d.lang
	var parts []string
	for _, s := range d.Sections {
		switch s.ID {
		case "new":
			parts = append(parts, l.n("subject.new", count(s)))
		case "regressed":
			parts = append(parts, l.n("subject.regressed", count(s)))
		case "crons":
			parts = append(parts, l.n("subject.crons", count(s)))
		case "uptime":
			parts = append(parts, l.n("subject.uptime", count(s)))
		}
	}
	if len(parts) == 0 {
		parts = append(parts, l.t("subject.quiet"))
	}
	return d.Title + ": " + strings.Join(parts, ", ")
}

// count es cuántos hay en la sección, también los que no salen.
func count(s Section) int64 {
	if s.Total > 0 {
		return s.Total
	}
	return int64(len(s.Items))
}

var mdEscaper = strings.NewReplacer(`\`, `\\`, "[", `\[`, "]", `\]`, "*", `\*`, "_", `\_`, "`", "\\`", "<", "&lt;", ">", "&gt;")

// Markdown es el resumen en Markdown, para el webhook, el MCP y los agentes.
func (d *Digest) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n%s\n", mdEscaper.Replace(oneLine(d.Title)), oneLine(d.Period))
	if d.Empty {
		fmt.Fprintf(&b, "\n%s\n", d.lang.t("empty"))
	}
	for _, s := range d.Sections {
		title := mdEscaper.Replace(oneLine(s.Title))
		if s.URL != "" {
			title = "[" + title + "](" + oneLine(s.URL) + ")"
		}
		fmt.Fprintf(&b, "\n## %s\n\n", title)
		if s.Note != "" {
			b.WriteString(mdEscaper.Replace(oneLine(s.Note)) + "\n\n")
		}
		for _, st := range s.Stats {
			fmt.Fprintf(&b, "- %s: **%s**", mdEscaper.Replace(oneLine(st.Label)), mdEscaper.Replace(oneLine(st.Value)))
			if st.Detail != "" {
				fmt.Fprintf(&b, " (%s)", mdEscaper.Replace(oneLine(st.Detail)))
			}
			b.WriteString("\n")
		}
		if len(s.Stats) > 0 && len(s.Items) > 0 {
			b.WriteString("\n")
		}
		for _, it := range s.Items {
			title := mdEscaper.Replace(oneLine(it.Title))
			if it.URL != "" {
				title = "[" + title + "](" + oneLine(it.URL) + ")"
			}
			b.WriteString("- " + title)
			if it.Detail != "" {
				b.WriteString(" — " + mdEscaper.Replace(oneLine(it.Detail)))
			}
			b.WriteString("\n")
		}
	}
	if d.URL != "" {
		fmt.Fprintf(&b, "\n[%s](%s)\n", d.lang.t("open"), d.URL)
	}
	return b.String()
}

// Text es el resumen en texto plano, la otra parte del correo.
func (d *Digest) Text() string {
	var b strings.Builder
	b.WriteString(oneLine(d.Title) + "\n" + oneLine(d.Period) + "\n")
	if d.Empty {
		b.WriteString("\n" + d.lang.t("empty") + "\n")
	}
	for _, s := range d.Sections {
		b.WriteString("\n" + strings.ToUpper(oneLine(s.Title)) + "\n")
		if s.URL != "" {
			b.WriteString(oneLine(s.URL) + "\n")
		}
		if s.Note != "" {
			b.WriteString(oneLine(s.Note) + "\n")
		}
		for _, st := range s.Stats {
			b.WriteString("  " + oneLine(st.Label) + ": " + oneLine(st.Value))
			if st.Detail != "" {
				b.WriteString(" (" + oneLine(st.Detail) + ")")
			}
			b.WriteString("\n")
		}
		for _, it := range s.Items {
			b.WriteString("- " + oneLine(it.Title) + "\n")
			if it.Detail != "" {
				b.WriteString("  " + oneLine(it.Detail) + "\n")
			}
			if it.URL != "" {
				b.WriteString("  " + oneLine(it.URL) + "\n")
			}
		}
	}
	b.WriteString("\n-- \n" + d.lang.t("footer", d.Project) + "\n")
	if d.SettingsURL != "" {
		b.WriteString(d.lang.t("footer.settings") + ": " + d.SettingsURL + "\n")
	}
	return b.String()
}

// Colores del correo: grises neutros y un azul para los enlaces.
const (
	cText   = "#1f2328"
	cMuted  = "#656d76"
	cSubtle = "#8c959f"
	cBorder = "#e4e7eb"
	cPanel  = "#f6f7f9"
	cLink   = "#2557d6"
	font    = "-apple-system,BlinkMacSystemFont,'Segoe UI',Inter,Helvetica,Arial,sans-serif"
)

// HTML es el resumen como correo en HTML sencillo, con los estilos en línea.
func (d *Digest) HTML() string {
	e := html.EscapeString
	var b strings.Builder
	fmt.Fprintf(&b, `<!DOCTYPE html>
<html lang="%s"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title></head>
<body style="margin:0;padding:0;background:#ffffff;color:%s;font-family:%s;font-size:14px;line-height:1.5">
<div style="max-width:600px;margin:0 auto;padding:28px 20px">
<p style="margin:0;color:%s;font-size:12px;font-weight:600;letter-spacing:.02em">intake-errors</p>
<h1 style="margin:6px 0 2px;font-size:20px;line-height:1.3;font-weight:600;color:%s">%s</h1>
<p style="margin:0;color:%s;font-size:13px">%s</p>
`, e(d.Language), e(d.Title), cText, font, cSubtle, cText, e(d.Title), cMuted, e(d.Period))
	if d.Empty {
		fmt.Fprintf(&b, `<p style="margin:20px 0 0;padding:14px 16px;border-radius:8px;background:%s;color:%s">%s</p>
`, cPanel, cMuted, e(d.lang.t("empty")))
	}
	for _, s := range d.Sections {
		title := e(s.Title)
		if s.URL != "" {
			title = fmt.Sprintf(`<a href="%s" style="color:%s;text-decoration:none">%s</a>`, e(s.URL), cLink, title)
		}
		fmt.Fprintf(&b, `<h2 style="margin:28px 0 8px;font-size:15px;font-weight:600;color:%s">%s</h2>
`, cText, title)
		if s.Note != "" {
			fmt.Fprintf(&b, `<p style="margin:-4px 0 8px;color:%s;font-size:12px">%s</p>
`, cSubtle, e(s.Note))
		}
		if len(s.Stats) > 0 {
			fmt.Fprintf(&b, `<table role="presentation" cellpadding="0" cellspacing="0" style="width:100%%;border-collapse:separate;border-spacing:0;border:1px solid %s;border-radius:8px"><tr>
`, cBorder)
			for i, st := range s.Stats {
				border := ""
				if i > 0 {
					border = "border-left:1px solid " + cBorder + ";"
				}
				fmt.Fprintf(&b, `<td style="%spadding:12px 14px;vertical-align:top"><div style="color:%s;font-size:12px">%s</div><div style="font-size:20px;font-weight:600;font-variant-numeric:tabular-nums">%s</div>`,
					border, cMuted, e(st.Label), e(st.Value))
				if st.Detail != "" {
					fmt.Fprintf(&b, `<div style="color:%s;font-size:12px">%s</div>`, cSubtle, e(st.Detail))
				}
				b.WriteString("</td>\n")
			}
			b.WriteString("</tr></table>\n")
		}
		if len(s.Items) > 0 {
			fmt.Fprintf(&b, `<table role="presentation" cellpadding="0" cellspacing="0" style="width:100%%;border-collapse:collapse;margin-top:%s">
`, map[bool]string{true: "10px", false: "0"}[len(s.Stats) > 0])
			for i, it := range s.Items {
				border := ""
				if i > 0 {
					border = "border-top:1px solid " + cBorder + ";"
				}
				title := e(oneLine(it.Title))
				if it.URL != "" {
					title = fmt.Sprintf(`<a href="%s" style="color:%s;text-decoration:none;font-weight:500">%s</a>`, e(it.URL), cLink, title)
				}
				fmt.Fprintf(&b, `<tr><td style="%spadding:9px 0"><div style="word-break:break-word">%s</div>`, border, title)
				if it.Detail != "" {
					fmt.Fprintf(&b, `<div style="color:%s;font-size:12px;word-break:break-word">%s</div>`, cMuted, e(oneLine(it.Detail)))
				}
				b.WriteString("</td></tr>\n")
			}
			b.WriteString("</table>\n")
		}
	}
	if d.URL != "" {
		fmt.Fprintf(&b, `<p style="margin:28px 0 0"><a href="%s" style="display:inline-block;padding:8px 14px;border-radius:6px;background:%s;color:#ffffff;text-decoration:none;font-weight:500;font-size:13px">%s</a></p>
`, e(d.URL), cText, e(d.lang.t("open")))
	}
	fmt.Fprintf(&b, `<p style="margin:28px 0 0;padding-top:14px;border-top:1px solid %s;color:%s;font-size:12px">%s`, cBorder, cSubtle, e(d.lang.t("footer", d.Project)))
	if d.SettingsURL != "" {
		fmt.Fprintf(&b, ` <a href="%s" style="color:%s">%s</a>`, e(d.SettingsURL), cSubtle, e(d.lang.t("footer.settings")))
	}
	b.WriteString("</p>\n</div>\n</body></html>\n")
	return b.String()
}

// oneLine deja s en una línea, sin saltos ni caracteres de control: lo que
// viene de un evento, como la versión de una release, no puede abrir otra
// sección del resumen.
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}
