import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import type { UptimeDetail } from '../../core/api';
import { I18n, lang } from '../../core/i18n';
import { spanishLeft } from '../../core/i18n.testing';
import { UptimeDetailPage } from './uptime-detail';

const now = Date.now();
const iso = (msAgo: number) => new Date(now - msAgo).toISOString();
const MIN = 60_000;
const SLOT = 15 * MIN;
const slot = (ago: number) => new Date(Math.floor(now / SLOT) * SLOT - ago * SLOT).toISOString();

const DETAIL: UptimeDetail = {
  id: 7,
  project_id: 4,
  project: 'web',
  slug: 'web-principal',
  name: 'Portada',
  url: 'https://example.com/',
  method: 'GET',
  interval_seconds: 60,
  timeout_seconds: 10,
  expected_status: '200-299',
  failure_threshold: 2,
  paused: false,
  status: 'down',
  consecutive_failures: 4,
  last_checked_at: iso(MIN),
  last_status_code: null,
  last_duration_ms: 3,
  last_error: 'conexión rechazada',
  next_check_at: iso(-MIN),
  created_at: iso(48 * 60 * MIN),
  updated_at: iso(48 * 60 * MIN),
  down_since: iso(4 * MIN),
  uptime_24h: 99.72,
  uptime_30d: 99.99,
  incidents: [
    {
      id: 2,
      started_at: iso(4 * MIN),
      ended_at: null,
      duration_ms: null,
      first_error: 'conexión rechazada',
    },
    {
      id: 1,
      started_at: iso(120 * MIN),
      ended_at: iso(117 * MIN),
      duration_ms: 3 * MIN,
      first_error: 'código 503',
    },
  ],
  checks: [
    {
      checked_at: iso(MIN),
      ok: false,
      status_code: null,
      duration_ms: 3,
      error: 'conexión rechazada',
    },
    { checked_at: iso(5 * MIN), ok: true, status_code: 200, duration_ms: 140 },
  ],
  response_times: [
    { at: slot(8), avg_ms: 120, max_ms: 180, checks: 15, failures: 0 },
    { at: slot(7), avg_ms: 200, max_ms: 260, checks: 15, failures: 3 },
    { at: slot(0), avg_ms: 140, max_ms: 140, checks: 5, failures: 4 },
  ],
};

describe('UptimeDetailPage', () => {
  let http: HttpTestingController;

  async function render() {
    const fixture = TestBed.createComponent(UptimeDetailPage);
    fixture.componentRef.setInput('project', '4');
    fixture.componentRef.setInput('slug', 'web-principal');
    fixture.detectChanges();
    http.expectOne('/api/0/projects/4/uptime-monitors/web-principal/').flush(DETAIL);
    await fixture.whenStable();
    fixture.detectChanges();
    return { fixture, el: fixture.nativeElement as HTMLElement };
  }

  beforeEach(() => {
    lang.set('es');
    TestBed.configureTestingModule({
      providers: [provideRouter([]), provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
  });

  afterEach(() => {
    http.verify();
    lang.set('es');
  });

  it('enseña el estado, el porcentaje, la gráfica y los incidentes', async () => {
    const { el } = await render();
    expect(el.querySelector('.head__name')?.textContent).toBe('Portada');
    expect(el.querySelector('.head .sp-badge')?.textContent).toBe('Caído');
    expect(el.querySelector('.head__since')?.textContent).toBe('caído desde hace 4 min');
    expect(el.querySelector('.last-error')?.textContent).toBe('Último error: conexión rechazada');
    const stats = Array.from(el.querySelectorAll('.stat')).map((s) =>
      Array.from(s.children)
        .map((c) => c.textContent?.trim().replace(/\s/g, ' '))
        .join(' | '),
    );
    expect(stats).toEqual([
      'Últimas 24 h | 99,72 %',
      'Últimos 30 días | 99,99 %',
      'Última respuesta | 3 ms | hace 1 min',
      'Comprobación | Cada minuto | Máximo 10 s · 200-299 · 2 fallos',
    ]);

    const chart = el.querySelector('app-response-chart svg')!;
    expect(chart.getAttribute('aria-label')).toBe(
      'Tiempo de respuesta medio en tramos de 15 minutos, hasta 200 ms; 2 tramos con fallos',
    );
    expect(chart.querySelectorAll('rect.hit')).toHaveLength(96);
    expect(chart.querySelectorAll('rect.fail')).toHaveLength(2);
    // Dos trozos con datos: los dos tramos seguidos de hace dos horas y el de ahora.
    expect(chart.querySelectorAll('path.line')).toHaveLength(2);
    expect(el.querySelector('app-response-chart .axis')?.textContent).toContain('Hace 24 h');

    const strip = el.querySelector('app-uptime-strip');
    expect(strip?.getAttribute('aria-label')).toBe('2 últimas comprobaciones, 1 con fallo');
    expect(strip?.querySelectorAll('span')).toHaveLength(60);

    const rows = Array.from(el.querySelectorAll('.incidents .history__row')).map((r) =>
      Array.from(r.querySelectorAll('[role=cell]')).map((c) => c.textContent?.trim()),
    );
    expect(rows[0][0]).toBe('hace 4 min');
    expect(rows[0][1]).toBe('En curso');
    expect(rows[0][2]).toMatch(/^4 min \d+ s$/);
    expect(rows[0][3]).toBe('conexión rechazada');
    expect(rows[1].slice(2)).toEqual(['3 min 0 s', 'código 503']);
    expect(el.querySelector('a[href="/monitors/4/uptime/web-principal/edit"]')).not.toBeNull();
  });

  it('pausa el monitor con PATCH', async () => {
    const { fixture, el } = await render();
    const btn = el.querySelector<HTMLButtonElement>('.titlebar__actions button')!;
    expect(btn.textContent).toContain('Pausar');
    btn.click();
    const req = http.expectOne('/api/0/projects/4/uptime-monitors/web-principal/');
    expect(req.request.method).toBe('PATCH');
    expect(req.request.body).toEqual({ paused: true });
    const { incidents: _, checks: __, response_times: ___, ...plain } = DETAIL;
    req.flush({ ...plain, paused: true });
    await fixture.whenStable();
    fixture.detectChanges();
    expect(btn.textContent).toContain('Reanudar');
    expect(el.querySelector('.head .sp-badge')?.textContent).toBe('Pausado');
    expect(el.querySelectorAll('.incidents .history__row')).toHaveLength(2);
  });

  it('si no existe lo dice', async () => {
    const fixture = TestBed.createComponent(UptimeDetailPage);
    fixture.componentRef.setInput('project', '4');
    fixture.componentRef.setInput('slug', 'nadie');
    fixture.detectChanges();
    http
      .expectOne('/api/0/projects/4/uptime-monitors/nadie/')
      .flush({ error: 'no encontrado' }, { status: 404, statusText: 'Not Found' });
    await fixture.whenStable();
    fixture.detectChanges();
    expect((fixture.nativeElement as HTMLElement).querySelector('.state__title')?.textContent).toBe(
      'Este monitor no existe',
    );
  });

  it('en inglés no queda texto en español', async () => {
    TestBed.inject(I18n).set('en');
    const { el } = await render();
    expect(el.querySelector('.head .sp-badge')?.textContent).toBe('Down');
    expect(el.textContent).toContain('Response time, last 24 h');
    expect(el.textContent).toContain('Ongoing');
    // Los errores de las comprobaciones llegan del servidor tal cual.
    for (const n of Array.from(
      el.querySelectorAll('.last-error, .incidents .mono, app-uptime-strip span'),
    )) {
      n.remove();
    }
    expect(spanishLeft(el)).toEqual([]);
  });
});
