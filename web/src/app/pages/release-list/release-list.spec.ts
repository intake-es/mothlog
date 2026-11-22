import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import type { Release } from '../../core/api';
import { EnvironmentChoice, environment } from '../../core/environment';
import { lang } from '../../core/i18n';
import { spanishLeft } from '../../core/i18n.testing';
import { ReleaseListPage } from './release-list';

const now = Date.now();
const iso = (msAgo: number) => new Date(now - msAgo).toISOString();
const HOUR = 3_600_000;

const release = (over: Partial<Release>): Release => ({
  id: 2,
  project_id: 4,
  project: 'web',
  version: 'web@1.1.0',
  first_seen: iso(3 * HOUR),
  finalized_at: iso(3 * HOUR),
  deploys: [
    { id: 3, environment: 'production', deployed_at: iso(2 * HOUR) },
    { id: 2, environment: 'staging', deployed_at: iso(3 * HOUR) },
    { id: 1, environment: 'production', deployed_at: iso(5 * HOUR) },
  ],
  new_issues: 2,
  issues: 3,
  regressions: 1,
  events: 1234,
  last_event: iso(60_000),
  ...over,
});

const RELEASES = [
  release({}),
  release({
    id: 1,
    version: 'web@1.0.0',
    finalized_at: null,
    deploys: [],
    new_issues: 0,
    regressions: 0,
    events: 7,
    first_seen: iso(30 * HOUR),
  }),
];

describe('ReleaseListPage', () => {
  let http: HttpTestingController;

  async function render(data: Release[] = RELEASES) {
    const fixture = TestBed.createComponent(ReleaseListPage);
    fixture.detectChanges();
    http.expectOne('/api/0/projects/').flush({ data: [{ id: 4, name: 'web', dsn: '' }] });
    const req = http.expectOne((r) => r.url === '/api/0/releases/');
    req.flush({ data });
    await fixture.whenStable();
    return { fixture, el: fixture.nativeElement as HTMLElement, params: req.request.params };
  }

  beforeEach(() => {
    lang.set('es');
    localStorage.clear();
    environment.set('');
    TestBed.configureTestingModule({
      providers: [provideRouter([]), provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
  });

  afterEach(() => {
    http.verify();
    environment.set('');
    localStorage.clear();
  });

  it('pinta cada release con su proyecto, sus despliegues y lo nuevo que trajo', async () => {
    const { el, params } = await render();
    expect(params.has('environment')).toBe(false);
    expect(el.querySelector('.titlebar__title')?.textContent).toContain('Releases');
    expect(el.querySelector('.titlebar__count')?.textContent).toBe('(2)');
    expect(Array.from(el.querySelectorAll('.list__head span')).map((s) => s.textContent)).toEqual([
      'Release',
      'Nuevos',
      'Regresiones',
      'Eventos',
      'Vista',
    ]);

    const rows = el.querySelectorAll<HTMLAnchorElement>('a.row');
    expect(rows.length).toBe(2);
    expect(rows[0].getAttribute('href')).toBe('/releases/4/web@1.1.0');
    expect(rows[0].querySelector('.row__version')?.textContent).toBe('web@1.1.0');
    expect(rows[0].querySelector('.sp-badge--gray')?.textContent).toBe('Finalizada');
    // El último despliegue de cada entorno.
    const deploys = Array.from(rows[0].querySelectorAll('.row__deploy'));
    expect(deploys.map((d) => d.textContent?.trim())).toEqual([
      'production · hace 2 h',
      'staging · hace 3 h',
    ]);
    expect(deploys[0].getAttribute('title')).toMatch(/^Desplegada en production: /);
    expect(rows[0].querySelector('.sp-badge--red')?.textContent).toBe('2');
    expect(rows[0].textContent).toContain('2 errores nuevos');
    expect(rows[0].querySelector('.sp-badge--yellow')?.textContent).toBe('1');
    expect(rows[0].textContent).toContain('1 regresión');
    expect(rows[0].textContent).toContain('1234');

    expect(rows[1].querySelector('.row__pending')?.textContent).toBe('Sin desplegar');
    expect(rows[1].textContent).toContain('Ningún error nuevo');
    expect(rows[1].querySelector('.sp-badge')).toBeNull();
  });

  it('pide solo las del entorno elegido y vuelve a pedir al cambiarlo', async () => {
    environment.set('staging');
    const { fixture, params } = await render();
    expect(params.get('environment')).toBe('staging');

    TestBed.inject(EnvironmentChoice).set('production');
    fixture.detectChanges();
    const req = http.expectOne((r) => r.url === '/api/0/releases/');
    expect(req.request.params.get('environment')).toBe('production');
    req.flush({ data: [] });
    await fixture.whenStable();
    const el: HTMLElement = fixture.nativeElement;
    expect(el.querySelector('.state__title')?.textContent).toBe(
      'Ninguna release con estos filtros',
    );
    expect(localStorage.getItem('ie-env')).toBe('production');
  });

  it('sin releases explica cómo se crean', async () => {
    const { el } = await render([]);
    expect(el.querySelector('.state__title')?.textContent).toBe('Todavía no hay releases');
    expect(el.querySelector('.state__text')?.textContent).toContain('sentry-cli releases new');
  });

  describe('en inglés', () => {
    it('pinta la cabecera, las filas y los despliegues en inglés', async () => {
      lang.set('en');
      const { el } = await render();
      expect(Array.from(el.querySelectorAll('.list__head span')).map((s) => s.textContent)).toEqual(
        ['Release', 'New', 'Regressions', 'Events', 'Seen'],
      );
      const rows = el.querySelectorAll<HTMLAnchorElement>('a.row');
      expect(rows[0].querySelector('.sp-badge--gray')?.textContent).toBe('Finalized');
      expect(rows[0].querySelector('.row__deploy')?.textContent?.trim()).toBe(
        'production · 2h ago',
      );
      expect(rows[0].textContent).toContain('2 new errors');
      expect(rows[0].textContent).toContain('1 regression');
      expect(rows[1].querySelector('.row__pending')?.textContent).toBe('Not deployed');
      expect(rows[1].textContent).toContain('No new errors');
      expect(el.querySelector('app-pick .pick')?.getAttribute('aria-label')).toBe(
        'Project: All projects',
      );
      expect(spanishLeft(el)).toEqual([]);
    });

    it('cambia de idioma sin volver a pedir las releases', async () => {
      const { fixture, el } = await render();
      expect(el.querySelector('.row__pending')?.textContent).toBe('Sin desplegar');
      lang.set('en');
      fixture.detectChanges();
      expect(el.querySelector('.row__pending')?.textContent).toBe('Not deployed');
      expect(spanishLeft(el)).toEqual([]);
    });
  });
});
