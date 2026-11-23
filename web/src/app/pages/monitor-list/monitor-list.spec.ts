import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import type { CheckInMark, Monitor, UptimeCheck, UptimeMonitor } from '../../core/api';
import { I18n, lang } from '../../core/i18n';
import { spanishLeft } from '../../core/i18n.testing';
import { MonitorListPage } from './monitor-list';

const now = Date.now();
const iso = (msAgo: number) => new Date(now - msAgo).toISOString();
const MIN = 60_000;

const marks = (...statuses: CheckInMark['status'][]): CheckInMark[] =>
  statuses.map((status, i) => ({ status, started_at: iso((i + 1) * 10 * MIN), duration_ms: 1200 }));

const monitor = (over: Partial<Monitor>): Monitor => ({
  id: 1,
  project_id: 4,
  project: 'api',
  slug: 'enviar-facturas',
  name: 'enviar-facturas',
  schedule: { type: 'interval', value: 2, unit: 'hour' },
  timezone: 'UTC',
  checkin_margin: 1,
  max_runtime: 30,
  environment: 'production',
  muted: false,
  status: 'error',
  last_checkin_at: iso(5 * MIN),
  next_checkin_at: new Date(now + 115 * MIN).toISOString(),
  created_at: iso(60 * MIN),
  updated_at: iso(60 * MIN),
  recent: marks('error', 'ok', 'ok'),
  ...over,
});

const MONITORS = [
  monitor({}),
  monitor({
    id: 2,
    slug: 'limpieza-nocturna',
    name: 'Limpieza nocturna',
    schedule: { type: 'crontab', value: '0 3 * * *' },
    timezone: 'Europe/Madrid',
    status: 'ok',
    muted: true,
    recent: marks('ok', 'missed', 'timeout', 'in_progress'),
  }),
  monitor({
    id: 3,
    slug: 'nuevo',
    name: 'nuevo',
    status: 'pending',
    last_checkin_at: null,
    next_checkin_at: null,
    recent: [],
  }),
];

const checks = (...ok: boolean[]): UptimeCheck[] =>
  ok.map((v, i) => ({
    checked_at: iso((i + 1) * MIN),
    ok: v,
    status_code: v ? 200 : 503,
    duration_ms: 180,
    error: v ? '' : 'código 503',
  }));

const uptime = (over: Partial<UptimeMonitor>): UptimeMonitor => ({
  id: 11,
  project_id: 4,
  project: 'api',
  slug: 'web-principal',
  name: 'Web principal',
  url: 'https://example.com/',
  method: 'GET',
  interval_seconds: 60,
  timeout_seconds: 10,
  expected_status: '200-299',
  failure_threshold: 2,
  paused: false,
  status: 'down',
  consecutive_failures: 3,
  last_checked_at: iso(MIN),
  last_status_code: 503,
  last_duration_ms: 180,
  last_error: 'código 503',
  next_check_at: iso(-MIN),
  created_at: iso(60 * MIN),
  updated_at: iso(60 * MIN),
  down_since: iso(3 * MIN),
  uptime_24h: 99.4583,
  uptime_30d: 99.98,
  recent: checks(false, false, false, true),
  ...over,
});

const UPTIME = [
  uptime({}),
  uptime({
    id: 12,
    slug: 'api',
    name: 'API',
    url: 'https://api.example.com/salud',
    method: 'HEAD',
    interval_seconds: 30,
    status: 'up',
    paused: true,
    down_since: null,
    uptime_24h: 100,
    uptime_30d: null,
    recent: checks(true),
  }),
];

describe('MonitorListPage', () => {
  let http: HttpTestingController;

  async function render(data: Monitor[] = MONITORS, project = '', up: UptimeMonitor[] = []) {
    const fixture = TestBed.createComponent(MonitorListPage);
    if (project) {
      fixture.componentRef.setInput('project', project);
    }
    fixture.detectChanges();
    http.expectOne('/api/0/projects/').flush({ data: [{ id: 4, name: 'api', dsn: '' }] });
    const upReq = http.expectOne((r) => r.url === '/api/0/uptime-monitors/');
    const req = http.expectOne((r) => r.url === '/api/0/monitors/');
    expect(upReq.request.params.get('project')).toBe(req.request.params.get('project'));
    upReq.flush({ data: up });
    req.flush({ data });
    await fixture.whenStable();
    fixture.detectChanges();
    return { fixture, el: fixture.nativeElement as HTMLElement, params: req.request.params };
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

  it('pinta cada monitor con su horario, su estado, la tira y cuándo toca', async () => {
    const { el, params } = await render();
    expect(params.has('project')).toBe(false);
    expect(el.querySelector('.titlebar__title')?.textContent).toContain('Monitores');
    expect(el.querySelector('.titlebar__count')?.textContent).toBe('(3)');
    expect(el.querySelector('.summary')?.textContent).toBe(
      '1 monitor falla, no llega o se pasa de tiempo.',
    );
    const rows = Array.from(el.querySelectorAll<HTMLAnchorElement>('.row'));
    expect(rows.map((r) => r.getAttribute('href'))).toEqual([
      '/monitors/4/enviar-facturas',
      '/monitors/4/limpieza-nocturna',
      '/monitors/4/nuevo',
    ]);
    expect(rows[0].querySelector('.row__name')?.textContent).toBe('enviar-facturas');
    expect(rows[0].querySelector('.row__meta')?.textContent).toContain('Cada 2 horas');
    expect(rows[0].querySelector('.row__status')?.textContent?.trim()).toBe('Falla');
    expect(rows[0].querySelectorAll('.row__when')[0].textContent?.trim()).toMatch(/dentro de 2 h/);
    expect(rows[0].querySelectorAll('.row__when')[1].textContent?.trim()).toBe('hace 5 min');

    // La tira va de la más vieja a la más nueva, con huecos vacíos a la izquierda.
    const bars = Array.from(rows[1].querySelectorAll('app-checkin-strip .bar'));
    expect(bars).toHaveLength(30);
    expect(bars.slice(-4).map((b) => b.className.replace('bar bar--', ''))).toEqual([
      'in_progress',
      'timeout',
      'missed',
      'ok',
    ]);
    expect(bars.at(-1)?.getAttribute('title')).toMatch(/^Bien · .* · hace 10 min · 1,2 s$/);
    expect(rows[1].querySelector('app-checkin-strip')?.getAttribute('aria-label')).toBe(
      '4 últimas ejecuciones, 2 con problemas',
    );
    expect(rows[1].querySelector('.row__meta .mono')?.textContent).toBe('0 3 * * *');
    expect(rows[1].querySelector('.sp-badge--gray')?.textContent).toContain('Silenciado');
    expect(rows[2].querySelector('.row__status')?.textContent?.trim()).toBe('Esperando');
    expect(rows[2].querySelectorAll('.row__when')[0].textContent?.trim()).toBe('—');
  });

  it('sin monitores explica qué son y da las dos formas de tener uno', async () => {
    const { fixture, el } = await render([]);
    expect(el.querySelector('.state__title')?.textContent).toBe('Todavía no hay monitores');
    expect(el.querySelector('.state__text')?.textContent).toBe(
      'Un monitor avisa cuando una web deja de responder o un cron no se ejecuta.',
    );
    expect(el.querySelector('.state')?.textContent).not.toMatch(/monitor_config|sentry-cli|README/);
    const ways = Array.from(el.querySelectorAll('.way'));
    expect(ways.map((w) => w.querySelector('.way__title')?.textContent)).toEqual([
      'Comprobar una web o una API',
      'Vigilar un cron',
    ]);
    const create = ways[0].querySelector<HTMLAnchorElement>('a.btn');
    expect(create?.getAttribute('href')).toBe('/monitors/uptime/new');
    expect(create?.textContent?.trim()).toBe('Nuevo monitor');
    // El icono del botón es el de los demás botones, no el grande del bloque vacío.
    expect(create?.querySelector('svg')?.parentElement).toBe(create);
    expect(el.querySelector('.state > svg[appIcon=heartbeat]')).not.toBeNull();

    const toggle = ways[1].querySelector<HTMLButtonElement>('.way__toggle')!;
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    expect(el.querySelector('#cron-example')).toBeNull();
    toggle.click();
    fixture.detectChanges();
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
    expect(toggle.textContent?.trim()).toBe('Ocultar el ejemplo');
    const code = el.querySelector('#cron-example code')?.textContent ?? '';
    expect(code).toContain('from sentry_sdk.crons import monitor');
    expect(code).toContain('monitor_slug="limpieza-nocturna"');

    let copied = '';
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: {
        writeText: (v: string) => {
          copied = v;
          return Promise.resolve();
        },
      },
    });
    el.querySelector<HTMLButtonElement>('.example__copy')?.click();
    await fixture.whenStable();
    await Promise.resolve();
    fixture.detectChanges();
    expect(copied).toBe(code);
    expect(el.querySelector('.example__copy')?.textContent?.trim()).toBe('Copiado');

    toggle.click();
    fixture.detectChanges();
    expect(el.querySelector('#cron-example')).toBeNull();
  });

  it('en inglés, el bloque vacío y su ejemplo tampoco dejan texto en español', async () => {
    TestBed.inject(I18n).set('en');
    const { fixture, el } = await render([]);
    expect(el.querySelector('.state__title')?.textContent).toBe('No monitors yet');
    el.querySelector<HTMLButtonElement>('.way__toggle')?.click();
    fixture.detectChanges();
    expect(el.querySelector('.way__toggle')?.textContent?.trim()).toBe('Hide the example');
    expect(el.querySelector('#cron-example code')?.textContent).toContain(
      'monitor_slug="nightly-cleanup"',
    );
    expect(spanishLeft(el)).toEqual([]);
  });

  it('filtra por proyecto', async () => {
    const { params } = await render(MONITORS.slice(0, 1), 'api');
    expect(params.get('project')).toBe('api');
  });

  it('en inglés no queda texto en español', async () => {
    TestBed.inject(I18n).set('en');
    const { el } = await render();
    expect(el.querySelector('.titlebar__title')?.textContent).toContain('Monitors');
    expect(el.querySelector('.row__meta')?.textContent).toContain('Every 2 hours');
    expect(el.querySelector('.row__status')?.textContent?.trim()).toBe('Failing');
    expect(el.querySelector('.summary')?.textContent).toBe(
      '1 monitor is failing, missed or timed out.',
    );
    expect(spanishLeft(el)).toEqual([]);
  });

  it('pinta los de disponibilidad antes que los crons, con su estado, la tira y el porcentaje', async () => {
    const { el } = await render(MONITORS, '', UPTIME);
    expect(el.querySelector('.titlebar__count')?.textContent).toBe('(5)');
    expect(el.querySelector('.summary')?.textContent).toBe(
      '1 monitor de disponibilidad caído. 1 monitor falla, no llega o se pasa de tiempo.',
    );
    expect(Array.from(el.querySelectorAll('h2.group')).map((h) => h.textContent?.trim())).toEqual([
      'Disponibilidad 2',
      'Crons 3',
    ]);
    const rows = Array.from(el.querySelectorAll<HTMLAnchorElement>('.row--uptime'));
    expect(rows.map((r) => r.getAttribute('href'))).toEqual([
      '/monitors/4/uptime/web-principal',
      '/monitors/4/uptime/api',
    ]);
    expect(rows[0].querySelector('.row__meta')?.textContent).toContain('GET example.com');
    expect(rows[0].querySelector('.row__meta')?.textContent).toContain('Cada minuto');
    expect(rows[0].querySelector('.row__status')?.textContent?.trim()).toBe('Caído');
    const pct = rows[0].querySelectorAll('.pct');
    const text = (n: Element | null | undefined) => n?.textContent?.trim().replace(/\s/g, ' ');
    expect(text(pct[0])).toBe('99,45 %');
    expect(pct[0].classList).toContain('pct--warn');
    expect(text(pct[1])).toBe('99,98 %');
    const bars = Array.from(rows[0].querySelectorAll('app-uptime-strip span'));
    expect(bars).toHaveLength(30);
    expect(bars.slice(-4).map((b) => b.className)).toEqual([
      'bar bar--ok',
      'bar bar--fail',
      'bar bar--fail',
      'bar bar--fail',
    ]);
    expect(bars.at(-1)?.getAttribute('title')).toMatch(/^Fallo · 503 · 180 ms · código 503 · /);
    expect(rows[0].querySelector('app-uptime-strip')?.getAttribute('aria-label')).toBe(
      '4 últimas comprobaciones, 3 con fallo',
    );
    expect(rows[1].querySelector('.row__status')?.textContent?.trim()).toBe('Pausado');
    expect(rows[1].querySelector('.row__meta')?.textContent).toContain(
      'HEAD api.example.com/salud',
    );
    expect(rows[1].querySelector('.row__meta')?.textContent).toContain('Cada 30 s');
    expect(Array.from(rows[1].querySelectorAll('.pct')).map(text)).toEqual(['100 %', '—']);
    expect(el.querySelector('a.btn--primary')?.getAttribute('href')).toBe('/monitors/uptime/new');
  });

  it('solo con los de disponibilidad no pinta el bloque vacío', async () => {
    const { el } = await render([], '', UPTIME.slice(0, 1));
    expect(el.querySelector('.state')).toBeNull();
    expect(el.querySelectorAll('.row')).toHaveLength(1);
    expect(el.querySelectorAll('h2.group')).toHaveLength(1);
  });

  it('en inglés, los de disponibilidad tampoco dejan texto en español', async () => {
    TestBed.inject(I18n).set('en');
    const { el } = await render(MONITORS, 'api', UPTIME);
    expect(el.querySelector('.summary')?.textContent).toBe(
      '1 uptime monitor is down. 1 monitor is failing, missed or timed out.',
    );
    const row = el.querySelector('.row--uptime');
    expect(row?.querySelector('.row__status')?.textContent?.trim()).toBe('Down');
    expect(row?.querySelector('.row__meta')?.textContent).toContain('Every minute');
    expect(row?.querySelector('.pct')?.textContent?.trim()).toBe('99.45%');
    expect(el.querySelector('a.btn--primary')?.textContent?.trim()).toBe('New monitor');
    expect(el.querySelector('a.btn--primary')?.getAttribute('href')).toBe(
      '/monitors/uptime/new?project=api',
    );
    // El error de la comprobación viene del servidor tal cual: se quita para mirar el resto.
    for (const b of Array.from(el.querySelectorAll('app-uptime-strip span'))) {
      b.removeAttribute('title');
    }
    expect(spanishLeft(el)).toEqual([]);
  });
});
