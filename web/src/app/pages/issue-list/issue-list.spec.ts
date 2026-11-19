import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { provideRouter, Router } from '@angular/router';
import type { Issue } from '../../core/api';
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
    TestBed.configureTestingModule({
      providers: [provideRouter([]), provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
  });

  afterEach(() => http.verify());

  it('pinta cada error con su tipo, mensaje, culprit, proyecto, release, eventos y minigráfica', async () => {
    const fixture = await render();
    const req = http.expectOne((r) => r.url === '/api/0/issues/');
    expect(req.request.params.get('status')).toBe('open');
    expect(req.request.params.get('daily')).toBe('1');
    req.flush({
      data: [
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
  });

  it('con 0 errores abiertos dice que no hay', async () => {
    const fixture = await render();
    http.expectOne((r) => r.url === '/api/0/issues/').flush({ data: [] });
    await fixture.whenStable();
    const el: HTMLElement = fixture.nativeElement;
    expect(el.querySelector('.state__title')?.textContent).toBe('No hay errores abiertos');
    expect(el.querySelector('.titlebar__count')?.textContent).toBe('(0)');
  });

  it('con filtros y sin resultados ofrece quitarlos', async () => {
    const fixture = await render({ query: 'nada', status: 'all', project: 'backend' });
    const req = http.expectOne((r) => r.url === '/api/0/issues/');
    expect(req.request.params.get('query')).toBe('nada');
    expect(req.request.params.get('status')).toBe('all');
    expect(req.request.params.get('project')).toBe('backend');
    req.flush({ data: [] });
    await fixture.whenStable();
    const el: HTMLElement = fixture.nativeElement;
    expect(el.querySelector('.state__title')?.textContent).toBe('Ningún error con estos filtros');
    expect((el.querySelector('.search input') as HTMLInputElement).value).toBe('nada');
  });

  it('cambiar el estado pone el filtro en la URL', async () => {
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
      expect.objectContaining({ queryParams: { status: 'resolved' } }),
    );
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
});
