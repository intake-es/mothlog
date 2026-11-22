import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import type { HealthHour, ReleaseDetail, ReleaseHealth, ReleaseIssue } from '../../core/api';
import { environment } from '../../core/environment';
import { lang } from '../../core/i18n';
import { spanishLeft } from '../../core/i18n.testing';
import { ReleaseDetailPage } from './release-detail';

const now = Date.now();
const iso = (msAgo: number) => new Date(now - msAgo).toISOString();
const HOUR = 3_600_000;

const issue = (over: Partial<ReleaseIssue>): ReleaseIssue => ({
  id: 7,
  project_id: 4,
  project: 'web',
  title: 'TypeError: Cannot read properties of undefined',
  culprit: 'src/carrito.ts in contarUnidades',
  type: 'TypeError',
  level: 'error',
  status: 'open',
  count: 5,
  user_count: 0,
  first_seen: iso(2 * HOUR),
  last_seen: iso(HOUR),
  last_release: 'web@1.1.0',
  resolved_in_release: null,
  last_regression: null,
  first_release: 'web@1.1.0',
  fixed_in_release: null,
  release_events: 3,
  release_last_seen: iso(HOUR),
  ...over,
});

const health = (over: Partial<ReleaseHealth> = {}): ReleaseHealth => ({
  environment: '',
  sessions: 0,
  sessions_errored: 0,
  sessions_crashed: 0,
  sessions_abnormal: 0,
  users: 0,
  users_crashed: 0,
  crash_free_sessions: null,
  crash_free_users: null,
  sessions_24h: 0,
  adoption: null,
  ...over,
});

/** 72 horas sin sesiones salvo la última, con 8 y una con crash. */
const series = (): HealthHour[] => {
  const end = Math.floor(now / HOUR) * HOUR;
  return Array.from({ length: 72 }, (_, i) => {
    const last = i === 71;
    return {
      hour: new Date(end - (71 - i) * HOUR).toISOString(),
      sessions: last ? 8 : 0,
      sessions_errored: last ? 2 : 0,
      sessions_crashed: last ? 1 : 0,
      sessions_abnormal: 0,
      crash_free_sessions: last ? 0.875 : null,
    };
  });
};

const withHealth = (over: Partial<ReleaseDetail> = {}): ReleaseDetail =>
  detail({
    health: health({
      sessions: 8,
      sessions_errored: 2,
      sessions_crashed: 1,
      users: 2,
      users_crashed: 1,
      crash_free_sessions: 0.875,
      crash_free_users: 0.5,
      sessions_24h: 8,
      adoption: 0.5,
    }),
    health_by_environment: [
      health({
        environment: 'production',
        sessions: 4,
        sessions_crashed: 1,
        crash_free_sessions: 0.75,
        users: 2,
        users_crashed: 1,
        crash_free_users: 0.5,
      }),
      health({ environment: 'staging', sessions: 4, crash_free_sessions: 1 }),
    ],
    health_series: series(),
    ...over,
  });

const spaces = (s: string | null | undefined) => (s ?? '').replace(/\s/g, ' ').trim();

const detail = (over: Partial<ReleaseDetail> = {}): ReleaseDetail => ({
  id: 2,
  project_id: 4,
  project: 'web',
  version: 'web@1.1.0',
  first_seen: iso(3 * HOUR),
  finalized_at: null,
  deploys: [{ id: 1, environment: 'production', deployed_at: iso(2 * HOUR) }],
  new_issues: 1,
  issues: 2,
  regressions: 0,
  events: 12,
  last_event: iso(HOUR),
  health: health(),
  health_by_environment: [],
  health_series: [],
  previous: 'web@1.0.0',
  next: null,
  new: [issue({})],
  regressed: [],
  continued: [issue({ id: 8, title: 'Sin tipo', type: '', first_release: 'web@1.0.0' })],
  stopped: [
    issue({
      id: 9,
      title: 'KeyError: oeste',
      type: 'KeyError',
      status: 'resolved',
      fixed_in_release: 'web@1.1.0',
      release_events: 1,
    }),
  ],
  ...over,
});

describe('ReleaseDetailPage', () => {
  let http: HttpTestingController;

  async function render(d: ReleaseDetail = detail()) {
    const fixture = TestBed.createComponent(ReleaseDetailPage);
    fixture.componentRef.setInput('project', '4');
    fixture.componentRef.setInput('version', d.version);
    fixture.detectChanges();
    const req = http.expectOne((r) => r.url === '/api/0/projects/4/releases/web%401.1.0/');
    req.flush(d);
    await fixture.whenStable();
    return { fixture, el: fixture.nativeElement as HTMLElement, params: req.request.params };
  }

  beforeEach(() => {
    lang.set('es');
    environment.set('');
    TestBed.configureTestingModule({
      providers: [provideRouter([]), provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
  });

  afterEach(() => {
    http.verify();
    environment.set('');
  });

  const titles = (el: HTMLElement) =>
    Array.from(el.querySelectorAll('.group .block__title')).map((h) =>
      h.childNodes[0]?.textContent?.trim(),
    );

  it('pinta la release, sus despliegues y los errores que trajo y los que dejaron de salir', async () => {
    const { el, params } = await render();
    expect(params.has('environment')).toBe(false);
    expect(el.querySelector('.crumbs__leaf')?.textContent).toBe('web@1.1.0');
    expect(el.querySelector('.head__version')?.textContent).toBe('web@1.1.0');
    expect(el.querySelector('.head__meta')?.textContent).toContain(
      'Vista por primera vez hace 3 h',
    );
    expect(el.querySelector('.deploy__env')?.textContent).toBe('production');
    expect(el.querySelector('.deploy__when')?.textContent).toBe('hace 2 h');

    expect(titles(el)).toEqual([
      'Errores nuevos',
      'Regresiones',
      'Siguen saliendo',
      'Dejaron de salir',
    ]);
    expect(Array.from(el.querySelectorAll('.stat__n')).map((n) => n.textContent)).toEqual([
      '1',
      '0',
      '1',
      '1',
      '12',
    ]);
    const groups = el.querySelectorAll('.group');
    const brought = groups[0].querySelector<HTMLAnchorElement>('a.issue');
    expect(brought?.getAttribute('href')).toBe('/issues/7');
    expect(brought?.querySelector('.issue__type')?.textContent).toBe('TypeError');
    expect(brought?.querySelector('.issue__count')?.textContent).toBe('3 eventos en esta release');
    expect(groups[1].querySelector('.muted')?.textContent).toBe('Ninguno.');
    expect(groups[3].querySelector('.group__hint')?.textContent).toBe(
      'Salían en web@1.0.0 y en esta ya no, o se resolvieron al desplegarla.',
    );
    expect(groups[3].querySelector('.sp-badge--green')?.textContent).toBe(
      'Resuelto con esta release',
    );
    expect(groups[3].querySelector('.issue__count')?.textContent).toBe('1 evento en web@1.0.0');

    const pager = el.querySelectorAll<HTMLAnchorElement>('.pager__btn');
    expect(pager[0].getAttribute('href')).toBe('/releases/4/web@1.0.0');
    expect(pager[1].classList).toContain('pager__btn--off');
  });

  it('pinta la salud: sesiones y usuarios sin crash, adopción, la serie y cada entorno', async () => {
    const { el } = await render(withHealth());
    const health = el.querySelector('.health') as HTMLElement;
    expect(health.querySelector('.block__title')?.textContent).toBe('Salud');
    const stats = Array.from(health.querySelectorAll('.stat'));
    expect(stats.map((s) => spaces(s.querySelector('.stat__n')?.textContent))).toEqual([
      '87,5 %',
      '50 %',
      '50 %',
    ]);
    expect(stats.map((s) => s.querySelector('.stat__label')?.textContent)).toEqual([
      'Sesiones sin crash',
      'Usuarios sin crash',
      'Adopción en 24 h',
    ]);
    expect(stats.map((s) => s.querySelector('.stat__sub')?.textContent)).toEqual([
      '1 de 8 con crash',
      '1 de 2 con crash',
      '8 sesiones en las últimas 24 h',
    ]);
    expect(stats[0].querySelector('.stat__n')?.getAttribute('data-tone')).toBe('tone-bad');

    const chart = health.querySelector('app-session-chart [role="img"]');
    expect(chart?.getAttribute('aria-label')).toBe(
      'Sesiones por hora en las últimas 72 h: 8 en total, 1 con crash',
    );
    expect(health.querySelectorAll('app-session-chart .col').length).toBe(72);
    expect(spaces(health.querySelectorAll('app-session-chart .tip')[71].textContent)).toMatch(
      /· 8 sesiones · 1 con crash · 87,5 %$/,
    );

    const rows = Array.from(health.querySelectorAll('.envs tbody tr')).map((r) =>
      Array.from(r.querySelectorAll('td')).map((td) => spaces(td.textContent)),
    );
    expect(rows).toEqual([
      ['production', '4', '1', '75 %', '50 %'],
      ['staging', '4', '0', '100 %', '—'],
    ]);
  });

  it('con un entorno elegido no repite la tabla por entorno', async () => {
    environment.set('production');
    const { el } = await render(
      withHealth({
        health: health({ environment: 'production', sessions: 4, crash_free_sessions: 0.75 }),
      }),
    );
    expect(el.querySelector('.envs')).toBeNull();
    expect(spaces(el.querySelector('.health .stat__sub')?.textContent)).toBe('0 de 4 con crash');
    expect(el.querySelectorAll('.health .stat__sub')[1].textContent).toBe(
      'Las sesiones no traen usuario',
    );
  });

  it('sin sesiones no enseña porcentajes', async () => {
    const { el } = await render();
    expect(el.querySelector('.health .stat')).toBeNull();
    expect(el.querySelector('.health .muted')?.textContent).toContain(
      'Todavía no hay sesiones de esta release',
    );
    expect(el.querySelector('.health')?.textContent).not.toContain('%');
  });

  it('pide la release del entorno elegido', async () => {
    environment.set('staging');
    const { el, params } = await render(detail({ deploys: [] }));
    expect(params.get('environment')).toBe('staging');
    expect(el.querySelector('.block .muted')?.textContent).toContain('Todavía no se ha desplegado');
  });

  it('una release que no existe', async () => {
    const fixture = TestBed.createComponent(ReleaseDetailPage);
    fixture.componentRef.setInput('project', '4');
    fixture.componentRef.setInput('version', 'web@1.1.0');
    fixture.detectChanges();
    http
      .expectOne((r) => r.url === '/api/0/projects/4/releases/web%401.1.0/')
      .flush({ error: 'no encontrado' }, { status: 404, statusText: 'Not Found' });
    await fixture.whenStable();
    expect(fixture.nativeElement.querySelector('.state__title')?.textContent).toBe(
      'Esta release no existe',
    );
  });

  describe('en inglés', () => {
    it('pinta los bloques, los recuentos y los despliegues en inglés', async () => {
      lang.set('en');
      const { el } = await render(detail({ previous: null }));
      expect(titles(el)).toEqual(['New errors', 'Regressions', 'Still happening', 'Stopped']);
      expect(el.querySelector('.head__meta')?.textContent).toContain('First seen 3h ago');
      expect(el.querySelector('.block__title')?.textContent?.trim()).toBe('Deploys');
      expect(el.querySelector('.issue__count')?.textContent).toBe('3 events in this release');
      expect(el.querySelectorAll('.group__hint')[3].textContent).toBe(
        'The ones resolved when it was deployed.',
      );
      expect(el.querySelector('.sp-badge--green')?.textContent).toBe('Resolved with this release');
      expect(el.querySelector('[aria-label="Back to releases"]')).not.toBeNull();
      expect(spanishLeft(el)).toEqual([]);
    });

    it('pinta la salud en inglés', async () => {
      lang.set('en');
      const { el } = await render(withHealth());
      const health = el.querySelector('.health') as HTMLElement;
      expect(health.querySelector('.block__title')?.textContent).toBe('Health');
      expect(
        Array.from(health.querySelectorAll('.stat')).map((s) => [
          spaces(s.querySelector('.stat__n')?.textContent),
          s.querySelector('.stat__label')?.textContent,
          s.querySelector('.stat__sub')?.textContent,
        ]),
      ).toEqual([
        ['87.5%', 'Crash-free sessions', '1 of 8 crashed'],
        ['50%', 'Crash-free users', '1 of 2 crashed'],
        ['50%', '24 h adoption', '8 sessions over the last 24 h'],
      ]);
      expect(
        health.querySelector('app-session-chart [role="img"]')?.getAttribute('aria-label'),
      ).toBe('Sessions per hour over the last 72 h: 8 in total, 1 crashed');
      expect(Array.from(health.querySelectorAll('.envs th')).map((th) => th.textContent)).toEqual([
        'Environment',
        'Sessions',
        'Crashed',
        'Crash-free sessions',
        'Crash-free users',
      ]);
      expect(spanishLeft(el)).toEqual([]);
    });

    it('sin sesiones lo explica en inglés', async () => {
      lang.set('en');
      const { el } = await render();
      expect(el.querySelector('.health .muted')?.textContent).toBe(
        'No sessions for this release yet. The SDKs send them when session tracking is turned on.',
      );
      expect(spanishLeft(el)).toEqual([]);
    });

    it('cambia de idioma sin volver a pedir la release', async () => {
      const { fixture, el } = await render();
      expect(titles(el)[0]).toBe('Errores nuevos');
      lang.set('en');
      fixture.detectChanges();
      expect(titles(el)[0]).toBe('New errors');
      expect(spanishLeft(el)).toEqual([]);
    });
  });
});
