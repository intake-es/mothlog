import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { provideRouter, Router } from '@angular/router';
import type { Issue } from '../../core/api';
import { I18n, lang } from '../../core/i18n';
import { spanishLeft } from '../../core/i18n.testing';
import { IssueListPage } from './issue-list';

const now = Date.now();
const iso = (msAgo: number) => new Date(now - msAgo).toISOString();

const issue = (over: Partial<Issue>): Issue => ({
  id: 1,
  project_id: 4,
  project: 'front',
  title: 'TypeError: Cannot read properties of undefined',
  culprit: 'https://app.example.com/chunk-Z554BZQ6.js in a',
  type: 'TypeError',
  level: 'error',
  status: 'open',
  count: 3,
  user_count: 0,
  first_seen: iso(3 * 86_400_000),
  last_seen: iso(5 * 60_000),
  last_release: 'fixture-release-1',
  resolved_in_release: null,
  last_regression: null,
  daily: Array.from({ length: 14 }, (_, i) => ({
    date: `2026-11-${10 + i}`,
    count: i === 13 ? 3 : 0,
  })),
  ...over,
});

describe('IssueListPage', () => {
  let http: HttpTestingController;

  async function render(inputs: Record<string, string> = {}) {
    const fixture = TestBed.createComponent(IssueListPage);
    for (const [k, v] of Object.entries(inputs)) {
      fixture.componentRef.setInput(k, v);
    }
    fixture.detectChanges();
    http.expectOne('/api/0/projects/').flush({
      data: [
        { id: 1, name: 'backend', dsn: '' },
        { id: 4, name: 'front', dsn: '' },
      ],
    });
    await fixture.whenStable();
    return fixture;
  }

  beforeEach(() => {
    lang.set('es');
    TestBed.configureTestingModule({
      providers: [provideRouter([]), provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
  });

  afterEach(() => http.verify());

  it('pinta cada error con su tipo, mensaje, culprit, proyecto, release, eventos y minigráfica', async () => {
    const fixture = await render();
    const req = http.expectOne((r) => r.url === '/api/0/issues/');
    expect(req.request.params.get('status')).toBe('all');
    expect(req.request.params.get('query')).toBe('is:open');
    expect(req.request.params.get('daily')).toBe('1');
    req.flush({
      data: [
        issue({ user_count: 3 }),
        issue({
          id: 2,
          title: 'ai.action_reported_failure',
          type: '',
          culprit: '',
          count: 12345,
          last_release: null,
          last_regression: iso(1000),
        }),
      ],
    });
    await fixture.whenStable();
    const el: HTMLElement = fixture.nativeElement;

    expect(el.querySelector('.titlebar__title')?.textContent).toContain('Errores');
    expect(el.querySelector('.titlebar__count')?.textContent).toBe('(2)');
    const rows = el.querySelectorAll<HTMLAnchorElement>('a.row');
    expect(rows.length).toBe(2);
    expect(rows[0].getAttribute('href')).toBe('/issues/1');
    expect(rows[0].querySelector('.row__type')?.textContent).toBe('TypeError');
    expect(rows[0].querySelector('.row__message')?.textContent).toBe(
      'Cannot read properties of undefined',
    );
    expect(rows[0].querySelector('.row__culprit')?.textContent).toBe(
      'https://app.example.com/chunk-Z554BZQ6.js in a',
    );
    expect(rows[0].textContent).toContain('front');
    expect(rows[0].textContent).toContain('fixture-release-1');
    expect(rows[0].querySelector('.row__seen time')?.textContent).toBe('hace 5 min');
    expect(rows[0].querySelectorAll('app-sparkline rect').length).toBe(14);
    expect(rows[1].querySelector('.row__type')).toBeNull();
    expect(rows[1].querySelector('.row__count')?.textContent).toContain('12.345');
    expect(rows[1].querySelector('.sp-badge--yellow')?.textContent).toBe('Regresión');
    expect(rows[0].querySelector('.row__users')?.textContent).toBe('3 usuarios');
    expect(rows[1].querySelector('.row__users')).toBeNull();
    expect((el.querySelector('app-search-bar input') as HTMLInputElement).value).toBe('is:open');
  });

  it('con 0 errores abiertos dice que no hay', async () => {
    const fixture = await render();
    http.expectOne((r) => r.url === '/api/0/issues/').flush({ data: [] });
    await fixture.whenStable();
    const el: HTMLElement = fixture.nativeElement;
    expect(el.querySelector('.state__title')?.textContent).toBe('No hay errores abiertos');
    expect(el.querySelector('.titlebar__count')?.textContent).toBe('(0)');
  });

  it('con filtros y sin resultados ofrece quitarlos; los enlaces de antes pasan a la búsqueda', async () => {
    const fixture = await render({ query: 'nada', status: 'all', project: 'backend' });
    const req = http.expectOne((r) => r.url === '/api/0/issues/');
    expect(req.request.params.get('query')).toBe('project:backend nada');
    expect(req.request.params.get('status')).toBe('all');
    expect(req.request.params.has('project')).toBe(false);
    req.flush({ data: [] });
    await fixture.whenStable();
    const el: HTMLElement = fixture.nativeElement;
    expect(el.querySelector('.state__title')?.textContent).toBe('Ningún error con estos filtros');
    expect((el.querySelector('app-search-bar input') as HTMLInputElement).value).toBe(
      'project:backend nada',
    );
    expect(el.querySelectorAll('app-pick .pick')[0].textContent).toContain('backend');
    expect(el.querySelectorAll('app-pick .pick')[1].textContent).toContain('Todos los estados');
  });

  it('los filtros en píldora escriben en la búsqueda', async () => {
    const fixture = await render();
    http.expectOne((r) => r.url === '/api/0/issues/').flush({ data: [] });
    await fixture.whenStable();
    const router = TestBed.inject(Router);
    const navigate = vi.spyOn(router, 'navigate').mockResolvedValue(true);
    const el: HTMLElement = fixture.nativeElement;
    const statusPick = el.querySelectorAll<HTMLButtonElement>('app-pick .pick')[1];
    expect(statusPick.textContent).toContain('Abiertos');
    statusPick.click();
    fixture.detectChanges();
    const resolved = Array.from(el.querySelectorAll<HTMLButtonElement>('.menu__item')).find((b) =>
      b.textContent?.includes('Resueltos'),
    );
    resolved?.click();
    expect(navigate).toHaveBeenCalledWith(
      [],
      expect.objectContaining({
        queryParams: { query: 'is:resolved', status: null, project: null },
      }),
    );

    const projectPick = el.querySelectorAll<HTMLButtonElement>('app-pick .pick')[0];
    projectPick.click();
    fixture.detectChanges();
    Array.from(el.querySelectorAll<HTMLButtonElement>('.menu__item'))
      .find((b) => b.textContent?.trim() === 'backend')
      ?.click();
    expect(navigate).toHaveBeenLastCalledWith(
      [],
      expect.objectContaining({
        queryParams: { query: 'project:backend is:open', status: null, project: null },
      }),
    );
  });

  it('«Míos» pone y quita assigned:me, y cada fila enseña a quién está asignada', async () => {
    const fixture = await render({ query: 'assigned:me is:open' });
    const req = http.expectOne((r) => r.url === '/api/0/issues/');
    expect(req.request.params.get('query')).toBe('assigned:me is:open');
    req.flush({
      data: [
        issue({ assignee: { name: 'dev', kind: 'user', display_name: 'Dev', active: true } }),
        issue({ id: 2, assignee: { name: 'agente', kind: 'token', active: false } }),
        issue({ id: 3 }),
      ],
    });
    await fixture.whenStable();
    const router = TestBed.inject(Router);
    const navigate = vi.spyOn(router, 'navigate').mockResolvedValue(true);
    const el: HTMLElement = fixture.nativeElement;
    const mine = el.querySelector<HTMLButtonElement>('.mine');
    expect(mine?.textContent?.trim()).toBe('Míos');
    expect(mine?.getAttribute('aria-pressed')).toBe('true');
    // La píldora de estado sigue en Abiertos.
    expect(el.querySelectorAll('app-pick .pick')[1].textContent).toContain('Abiertos');

    const rows = el.querySelectorAll('a.row');
    expect(rows[0].querySelector('.row__who')?.getAttribute('title')).toBe('Asignado a Dev');
    expect(rows[0].querySelector('app-avatar')?.textContent?.trim()).toBe('D');
    expect(rows[1].querySelector('.row__who')?.getAttribute('title')).toBe(
      'Asignado a agente (token revocado)',
    );
    expect(rows[1].querySelector('app-avatar svg')).not.toBeNull();
    expect(rows[2].querySelector('app-avatar')).toBeNull();

    mine?.click();
    expect(navigate).toHaveBeenLastCalledWith(
      [],
      expect.objectContaining({ queryParams: { query: null, status: null, project: null } }),
    );
    fixture.componentRef.setInput('query', 'is:open');
    await fixture.whenStable();
    http.expectOne((r) => r.url === '/api/0/issues/').flush({ data: [] });
    await fixture.whenStable();
    expect(mine?.getAttribute('aria-pressed')).toBe('false');
    mine?.click();
    expect(navigate).toHaveBeenLastCalledWith(
      [],
      expect.objectContaining({
        queryParams: { query: 'assigned:me is:open', status: null, project: null },
      }),
    );
  });

  it('un error de sintaxis se ve en la barra y en la lista, en los dos idiomas', async () => {
    const fixture = await render({ query: 'os:"Mac OS' });
    const req = http.expectOne((r) => r.url === '/api/0/issues/');
    expect(req.request.params.get('query')).toBe('os:"Mac OS');
    req.flush(
      {
        error: 'parámetro no válido: búsqueda, carácter 4: faltan las comillas de cierre',
        query_error: { code: 'unclosed_quote', position: 3 },
      },
      { status: 400, statusText: 'Bad Request' },
    );
    await fixture.whenStable();
    const el: HTMLElement = fixture.nativeElement;
    expect(el.querySelector('.state__title')?.textContent).toBe('La búsqueda no es válida');
    expect(el.querySelector('#search-error')?.textContent?.trim()).toBe(
      'Carácter 4: faltan las comillas de cierre.',
    );
    TestBed.inject(I18n).set('en');
    await fixture.whenStable();
    expect(el.querySelector('.state__title')?.textContent).toBe('The search isn’t valid');
    expect(el.querySelector('#search-error')?.textContent?.trim()).toBe(
      'Character 4: the closing quote is missing.',
    );
    expect(spanishLeft(el)).toEqual([]);
  });

  it('muestra el error de la API y deja reintentar', async () => {
    const fixture = await render();
    http
      .expectOne((r) => r.url === '/api/0/issues/')
      .flush({ error: 'error interno' }, { status: 500, statusText: 'x' });
    await fixture.whenStable();
    const el: HTMLElement = fixture.nativeElement;
    expect(el.querySelector('[role=alert]')?.textContent).toContain('error interno');
    (el.querySelector('[role=alert] button') as HTMLButtonElement).click();
    http.expectOne((r) => r.url === '/api/0/issues/').flush({ data: [issue({})] });
    await fixture.whenStable();
    expect(el.querySelectorAll('a.row').length).toBe(1);
  });

  it('carga la página siguiente con el cursor', async () => {
    const fixture = await render();
    http
      .expectOne((r) => r.url === '/api/0/issues/')
      .flush({ data: [issue({})], next_cursor: 'c1' });
    await fixture.whenStable();
    const el: HTMLElement = fixture.nativeElement;
    expect(el.querySelector('.titlebar__count')?.textContent).toBe('(1+)');
    (el.querySelector('.more button') as HTMLButtonElement).click();
    const req = http.expectOne(
      (r) => r.url === '/api/0/issues/' && r.params.get('cursor') === 'c1',
    );
    req.flush({ data: [issue({ id: 9 })] });
    await fixture.whenStable();
    expect(el.querySelectorAll('a.row').length).toBe(2);
    expect(el.querySelector('.more')).toBeNull();
  });

  describe('en inglés', () => {
    const rowsData = () => [
      issue({}),
      issue({
        id: 2,
        title: 'ai.action_reported_failure',
        type: '',
        culprit: '',
        count: 12345,
        last_release: null,
        last_regression: iso(1000),
      }),
      issue({ id: 3, status: 'resolved_next', count: 1 }),
    ];

    it('pinta la cabecera, los filtros, las filas y las fechas en inglés', async () => {
      lang.set('en');
      const fixture = await render({ status: 'all' });
      http.expectOne((r) => r.url === '/api/0/issues/').flush({ data: rowsData() });
      await fixture.whenStable();
      const el: HTMLElement = fixture.nativeElement;

      expect(el.querySelector('.titlebar__title')?.textContent).toContain('Errors');
      const picks = Array.from(el.querySelectorAll('app-pick .pick')).map((p) =>
        p.getAttribute('aria-label'),
      );
      expect(picks).toEqual(['Project: All projects', 'Status: All statuses', 'Sort: Last seen']);
      expect(el.querySelector('app-search-bar .visually-hidden')?.textContent).toBe('Search');
      expect(Array.from(el.querySelectorAll('.list__head span')).map((s) => s.textContent)).toEqual(
        ['Error', '14 days', 'Events', 'Seen'],
      );
      const rows = el.querySelectorAll('a.row');
      expect(rows[0].querySelector('.row__seen time')?.textContent).toBe('5m ago');
      expect(rows[0].querySelector('.row__first')?.textContent).toBe('first 3d ago');
      expect(rows[0].querySelector('.row__seen time')?.getAttribute('title')).toMatch(
        /^Last seen: /,
      );
      expect(rows[1].querySelector('.row__count')?.textContent).toBe('12,345 events');
      expect(rows[1].querySelector('.sp-badge--yellow')?.textContent).toBe('Regression');
      expect(rows[2].querySelector('.row__count')?.textContent).toBe('1 event');
      expect(rows[2].querySelector('.sp-badge')?.textContent).toBe('Resolved in the next release');
      expect(rows[0].querySelector('app-sparkline svg')?.getAttribute('aria-label')).toBe(
        '3 events in the last 14 days',
      );
      expect(spanishLeft(el)).toEqual([]);
    });

    it('«Míos» y el asignado de cada fila, en inglés', async () => {
      lang.set('en');
      const fixture = await render({ query: 'assigned:me' });
      http
        .expectOne((r) => r.url === '/api/0/issues/')
        .flush({
          data: [issue({ assignee: { name: 'agente', kind: 'token', active: false } })],
        });
      await fixture.whenStable();
      const el: HTMLElement = fixture.nativeElement;
      expect(el.querySelector('.mine')?.textContent?.trim()).toBe('Mine');
      expect(el.querySelector('.mine')?.getAttribute('title')).toBe(
        'Errors assigned to you (assigned:me)',
      );
      expect(el.querySelector('.row__who')?.getAttribute('title')).toBe(
        'Assigned to agente (revoked token)',
      );
      expect(spanishLeft(el)).toEqual([]);
    });

    it('los estados vacíos y el error, en inglés', async () => {
      lang.set('en');
      const fixture = await render({ query: 'nada' });
      http.expectOne((r) => r.url === '/api/0/issues/').flush({ data: [] });
      await fixture.whenStable();
      const el: HTMLElement = fixture.nativeElement;
      expect(el.querySelector('.state__title')?.textContent).toBe('No errors match these filters');
      expect(el.querySelector('.state .btn')?.textContent).toBe('Clear filters');
      expect(el.querySelector('app-search-bar input')?.getAttribute('placeholder')).toBe(
        'Search: text, is:open, browser:Safari…',
      );
      expect(spanishLeft(el)).toEqual([]);

      fixture.componentRef.setInput('query', undefined);
      await fixture.whenStable();
      http.expectOne((r) => r.url === '/api/0/issues/').flush({ data: [] });
      await fixture.whenStable();
      expect(el.querySelector('.state__title')?.textContent).toBe('No open errors');

      fixture.componentRef.setInput('status', 'resolved');
      await fixture.whenStable();
      http
        .expectOne((r) => r.url === '/api/0/issues/')
        .error(new ProgressEvent('error'), { status: 0, statusText: '' });
      await fixture.whenStable();
      expect(el.querySelector('[role=alert] .state__title')?.textContent).toBe(
        'Couldn’t load the errors',
      );
      expect(el.querySelector('[role=alert] .state__text')?.textContent).toBe(
        'Can’t reach the server.',
      );
      expect(el.querySelector('[role=alert] button')?.textContent).toBe('Retry');
      expect(spanishLeft(el)).toEqual([]);
    });

    it('cambia de idioma sin recargar ni volver a pedir la lista', async () => {
      const fixture = await render();
      http.expectOne((r) => r.url === '/api/0/issues/').flush({ data: rowsData() });
      await fixture.whenStable();
      const el: HTMLElement = fixture.nativeElement;
      expect(el.querySelector('.titlebar__title')?.textContent).toContain('Errores');
      expect(el.querySelectorAll('a.row')[1].querySelector('.row__count')?.textContent).toBe(
        '12.345 eventos',
      );

      TestBed.inject(I18n).set('en');
      await fixture.whenStable();
      expect(el.querySelector('.titlebar__title')?.textContent).toContain('Errors');
      expect(el.querySelectorAll('a.row')[1].querySelector('.row__count')?.textContent).toBe(
        '12,345 events',
      );
      expect(el.querySelector('a.row .row__seen time')?.textContent).toBe('5m ago');
      expect(el.querySelectorAll('app-pick .pick')[1].textContent).toContain('Open');
      expect(spanishLeft(el)).toEqual([]);

      TestBed.inject(I18n).set('es');
      await fixture.whenStable();
      expect(el.querySelector('a.row .row__seen time')?.textContent).toBe('hace 5 min');
      expect(el.querySelectorAll('app-pick .pick')[1].textContent).toContain('Abiertos');
      // http.verify() en afterEach: ningún cambio de idioma ha pedido nada al servidor.
    });
  });
});
