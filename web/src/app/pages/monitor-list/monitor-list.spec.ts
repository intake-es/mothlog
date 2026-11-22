import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import type { CheckInMark, Monitor } from '../../core/api';
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

describe('MonitorListPage', () => {
  let http: HttpTestingController;

  async function render(data: Monitor[] = MONITORS, project = '') {
    const fixture = TestBed.createComponent(MonitorListPage);
    if (project) {
      fixture.componentRef.setInput('project', project);
    }
    fixture.detectChanges();
    http.expectOne('/api/0/projects/').flush({ data: [{ id: 4, name: 'api', dsn: '' }] });
    const req = http.expectOne((r) => r.url === '/api/0/monitors/');
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

  it('sin monitores dice cómo se crean', async () => {
    const { el } = await render([]);
    expect(el.querySelector('.state__title')?.textContent).toBe('Todavía no hay monitores');
    expect(el.querySelector('.state__text')?.textContent).toContain('sentry-cli monitors run');
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
});
