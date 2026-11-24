package digest

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Idiomas del resumen.
const (
	Spanish = "es"
	English = "en"
)

// texts son los textos del resumen por idioma; los que llevan %s o %d van con fmt.
var texts = map[string]map[string]string{
	Spanish: {
		"title.daily":       "Resumen diario de %s",
		"title.weekly":      "Resumen semanal de %s",
		"period":            "Del %s al %s (%s)",
		"empty":             "Sin novedades en el periodo: ni errores nuevos, ni regresiones, ni crons ni webs con problemas.",
		"open":              "Abrir Mothlog",
		"totals":            "En números",
		"totals.events":     "Eventos",
		"totals.issues":     "Errores con eventos",
		"totals.users":      "Usuarios afectados",
		"totals.resolved":   "Resueltos",
		"totals.vs":         "%s respecto al periodo anterior",
		"totals.vsNone":     "ninguno en el periodo anterior",
		"new":               "Errores nuevos",
		"regressed":         "Han vuelto",
		"growing":           "Los que más crecen",
		"top":               "Los %d con más eventos de %d.",
		"top.crons":         "Los %d con más fallos de %d.",
		"growing.detail":    "%s (antes %s)",
		"regressed.release": "con %s",
		"crons":             "Crons con problemas",
		"uptime":            "Webs que no respondieron",
		"top.uptime":        "Las %d con más tiempo caídas de %d.",
		"uptime.incidents":  "%s caída|%s caídas",
		"uptime.down":       "%s sin responder",
		"uptime.now.down":   "ahora: caída",
		"uptime.now.up":     "ahora: responde",
		"subject.uptime":    "%s web caída|%s webs caídas",
		"crons.failed":      "%s fallido|%s fallidos",
		"crons.missed":      "%s sin llegar|%s sin llegar",
		"crons.timeout":     "%s pasado de tiempo|%s pasados de tiempo",
		"crons.now":         "ahora: %s",
		"status.ok":         "va bien",
		"status.error":      "fallando",
		"status.missed":     "sin llegar",
		"status.timeout":    "pasado de tiempo",
		"status.pending":    "esperando",
		"health":            "Salud de la release %s",
		"health.sessions":   "Sesiones sin crash",
		"health.users":      "Usuarios sin crash",
		"health.total":      "Sesiones",
		"health.crashed":    "%s con crash",
		"events":            "%s evento|%s eventos",
		"users":             "%s usuario|%s usuarios",
		"subject.new":       "%s nuevo|%s nuevos",
		"subject.regressed": "%s ha vuelto|%s han vuelto",
		"subject.crons":     "%s cron con problemas|%s crons con problemas",
		"subject.quiet":     "sin novedades",
		"footer":            "Lo manda Mothlog con la configuración del proyecto %s.",
		"footer.settings":   "Cambiar el resumen",
		"weekday.short":     "dom,lun,mar,mié,jue,vie,sáb",
		"month.short":       "ene,feb,mar,abr,may,jun,jul,ago,sep,oct,nov,dic",
		"date":              "%[1]s %[2]d %[3]s, %[4]s",
		"thousands":         ".",
		"decimal":           ",",
		"percent":           "%s %%",
	},
	English: {
		"title.daily":       "Daily digest for %s",
		"title.weekly":      "Weekly digest for %s",
		"period":            "From %s to %s (%s)",
		"empty":             "Nothing new in this period: no new errors, no regressions and no cron or uptime problems.",
		"open":              "Open Mothlog",
		"totals":            "By the numbers",
		"totals.events":     "Events",
		"totals.issues":     "Errors with events",
		"totals.users":      "Affected users",
		"totals.resolved":   "Resolved",
		"totals.vs":         "%s vs. the previous period",
		"totals.vsNone":     "none in the previous period",
		"new":               "New errors",
		"regressed":         "Regressions",
		"growing":           "Growing fastest",
		"top":               "Top %d of %d by events.",
		"top.crons":         "Top %d of %d by failures.",
		"growing.detail":    "%s (%s before)",
		"regressed.release": "in %s",
		"crons":             "Cron problems",
		"uptime":            "Downtime",
		"top.uptime":        "Top %d of %d by downtime.",
		"uptime.incidents":  "%s incident|%s incidents",
		"uptime.down":       "%s down",
		"uptime.now.down":   "now: down",
		"uptime.now.up":     "now: up",
		"subject.uptime":    "%s site down|%s sites down",
		"crons.failed":      "%s failed|%s failed",
		"crons.missed":      "%s missed|%s missed",
		"crons.timeout":     "%s timed out|%s timed out",
		"crons.now":         "now: %s",
		"status.ok":         "ok",
		"status.error":      "failing",
		"status.missed":     "missed",
		"status.timeout":    "timed out",
		"status.pending":    "pending",
		"health":            "Release health: %s",
		"health.sessions":   "Crash-free sessions",
		"health.users":      "Crash-free users",
		"health.total":      "Sessions",
		"health.crashed":    "%s crashed",
		"events":            "%s event|%s events",
		"users":             "%s user|%s users",
		"subject.new":       "%s new|%s new",
		"subject.regressed": "%s regression|%s regressions",
		"subject.crons":     "%s cron problem|%s cron problems",
		"subject.quiet":     "nothing new",
		"footer":            "Sent by Mothlog with the settings of project %s.",
		"footer.settings":   "Change the digest",
		"weekday.short":     "Sun,Mon,Tue,Wed,Thu,Fri,Sat",
		"month.short":       "Jan,Feb,Mar,Apr,May,Jun,Jul,Aug,Sep,Oct,Nov,Dec",
		"date":              "%[1]s, %[3]s %[2]d, %[4]s",
		"thousands":         ",",
		"decimal":           ".",
		"percent":           "%s%%",
	},
}

// tr traduce los textos del resumen a un idioma.
type tr string

func (l tr) dict() map[string]string {
	if d, ok := texts[string(l)]; ok {
		return d
	}
	return texts[English]
}

func (l tr) t(key string, args ...any) string {
	s, ok := l.dict()[key]
	if !ok {
		panic("digest: missing text " + key)
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

// n es «1 evento» o «3 eventos»: la clave lleva las dos formas separadas por |.
func (l tr) n(key string, n int64) string {
	one, other, _ := strings.Cut(l.t(key), "|")
	if n == 1 {
		return fmt.Sprintf(one, l.num(n))
	}
	return fmt.Sprintf(other, l.num(n))
}

// num es el número con el separador de miles del idioma.
func (l tr) num(n int64) string {
	s := strconv.FormatInt(max(n, -n), 10)
	var b strings.Builder
	if n < 0 {
		b.WriteByte('-')
	}
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteString(l.t("thousands"))
		}
		b.WriteRune(r)
	}
	return b.String()
}

// pct es un tanto por uno como porcentaje con un decimal: «98,5 %».
func (l tr) pct(v float64) string {
	s := strconv.FormatFloat(v*100, 'f', 1, 64)
	s = strings.TrimSuffix(s, ".0")
	return l.t("percent", strings.Replace(s, ".", l.t("decimal"), 1))
}

// change es «+12 %» o «−40 %» del periodo anterior a este.
func (l tr) change(cur, prev int64) string {
	if prev == 0 {
		return l.t("totals.vsNone")
	}
	d := float64(cur-prev) / float64(prev)
	sign := "+"
	if d < 0 {
		sign, d = "−", -d
	}
	return l.t("totals.vs", sign+l.pct(float64(int(d*100+0.5))/100))
}

// date es «lun 21 nov, 9:00» o «Mon, Nov 21, 9:00».
func (l tr) date(t time.Time) string {
	days := strings.Split(l.t("weekday.short"), ",")
	months := strings.Split(l.t("month.short"), ",")
	return fmt.Sprintf(l.t("date"), days[t.Weekday()], t.Day(), months[t.Month()-1], t.Format("15:04"))
}
