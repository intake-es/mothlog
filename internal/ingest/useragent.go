package ingest

import "strings"

// UserAgent es lo que se saca de un user agent: navegador, sistema y dispositivo.
// Lo que no se reconoce queda vacío.
type UserAgent struct {
	Browser string
	OS      string
	Device  string
}

// El orden importa: Edge, Opera y Samsung Internet también dicen Chrome, y
// casi todos dicen Safari.
var browsers = []struct{ token, name string }{
	{"Edg/", "Edge"},
	{"EdgA/", "Edge"},
	{"EdgiOS/", "Edge"},
	{"OPR/", "Opera"},
	{"SamsungBrowser/", "Samsung Internet"},
	{"Firefox/", "Firefox"},
	{"FxiOS/", "Firefox"},
	{"HeadlessChrome/", "Chrome Headless"},
	{"CriOS/", "Chrome"},
	{"Chrome/", "Chrome"},
	{"Chromium/", "Chromium"},
}

func ParseUserAgent(ua string) UserAgent {
	var out UserAgent
	if ua == "" {
		return out
	}
	for _, b := range browsers {
		if strings.Contains(ua, b.token) {
			out.Browser = b.name
			break
		}
	}
	if out.Browser == "" && strings.Contains(ua, "Safari/") && strings.Contains(ua, "Version/") {
		out.Browser = "Safari"
		if strings.Contains(ua, "Mobile/") {
			out.Browser = "Mobile Safari"
		}
	}

	switch {
	case strings.Contains(ua, "iPhone"):
		out.OS, out.Device = "iOS", "iPhone"
	case strings.Contains(ua, "iPad"):
		out.OS, out.Device = "iOS", "iPad"
	case strings.Contains(ua, "Android"):
		out.OS, out.Device = "Android", androidModel(ua)
	case strings.Contains(ua, "CrOS"):
		out.OS = "Chrome OS"
	case strings.Contains(ua, "Windows"):
		out.OS = "Windows"
	case strings.Contains(ua, "Macintosh") || strings.Contains(ua, "Mac OS X"):
		out.OS, out.Device = "Mac OS", "Mac"
	case strings.Contains(ua, "Linux"):
		out.OS = "Linux"
	}
	return out
}

// androidModel saca «Pixel 8» de «(Linux; Android 14; Pixel 8 Build/…)». Con
// el user agent reducido de Chrome el modelo es «K» y no dice nada.
func androidModel(ua string) string {
	start := strings.Index(ua, "(")
	end := strings.Index(ua, ")")
	if start < 0 || end < start {
		return ""
	}
	parts := strings.Split(ua[start+1:end], ";")
	for i, p := range parts {
		if strings.HasPrefix(strings.TrimSpace(p), "Android") && i+1 < len(parts) {
			m := strings.TrimSpace(parts[i+1])
			if j := strings.Index(m, " Build/"); j >= 0 {
				m = m[:j]
			}
			if m == "K" || m == "U" || m == "wv" {
				return ""
			}
			return m
		}
	}
	return ""
}
