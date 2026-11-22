import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import type { HistoryEntry, IssueDetail, IssueTags, StoredEvent } from '../../core/api';
import { environment } from '../../core/environment';
import { I18n, lang } from '../../core/i18n';
import { spanishLeft } from '../../core/i18n.testing';
import { IssueDetailPage } from './issue-detail';

const stored = (id: number, release: string, line: number): StoredEvent => ({
  id,
  issue_id: 5,
  project_id: 4,
  event_id: `${id}`.padStart(32, 'a'),
  occurred_at: new Date(Date.now() - id * 60_000).toISOString(),
  received_at: new Date().toISOString(),
  event: {
    event_id: `${id}`.padStart(32, 'a'),
    timestamp: new Date().toISOString(),
    level: 'error',
    release,
    environment: 'production',
    sdk: { name: 'sentry.javascript.angular', version: '11.0.0' },
    exceptions: [
      {
        type: 'TypeError',
        value: "Cannot read properties of undefined (reading 'reduce')",
        frames: [
          {
            filename: 'node_modules/mitt/src/index.ts',
            function: 'Object.emit',
            lineno: 1,
            in_app: false,
          },
          {
            filename: 'src/carrito.ts',
            function: 'a',
            lineno: line,
            in_app: true,
            pre_context: ['  x'],
            context_line:
              '  return pedido.lineas!.reduce((total, linea) => total + linea.cantidad, 0);',
            post_context: ['}'],
          },
        ],
      },
    ],
    tags: { organization_id: '42', browser: 'Chrome' },
    user: { email: 'ana@example.com' },
    request: { url: 'https://app.example.com/' },
    breadcrumbs: [
      {
        timestamp: 1795090879,
        category: 'navigation',
        data: { from: '/', to: '/pedidos' },
      },
    ],
  },
});

const detail = (over: Partial<IssueDetail> = {}): IssueDetail => ({
  id: 5,
  project_id: 4,
  project: 'front',
  title: "TypeError: Cannot read properties of undefined (reading 'reduce')",
  culprit: 'https://app.example.com/chunk-Z554BZQ6.js in a',
  type: 'TypeError',
  level: 'error',
  status: 'open',
  count: 2,
  user_count: 3,
  first_seen: new Date(Date.now() - 86_400_000).toISOString(),
  last_seen: new Date().toISOString(),
  last_release: 'fixture-release-1',
  resolved_in_release: null,
  last_regression: null,
  latest_event: stored(1, 'fixture-release-1', 7),
  daily: Array.from({ length: 14 }, (_, i) => ({
    date: `2026-11-${String(6 + i).padStart(2, '0')}`,
    count: i === 13 ? 2 : 0,
  })),
  top_values: {
    release: [{ value: 'fixture-release-1', count: 2 }],
    environment: [],
    user: [],
    organization_id: [],
  },
  ...over,
});

const issueTags = (): IssueTags => ({
  issue_id: 5,
  events: 4,
  user_count: 3,
  tags: [
    {
      key: 'release',
      total: 4,
      distinct: 1,
      values: [{ value: 'fixture-release-1', count: 4, percent: 100, last_seen: '' }],
    },
    {
      key: 'browser',
      total: 4,
      distinct: 2,
      values: [
        { value: 'Safari', count: 3, percent: 75, last_seen: '' },
        { value: 'Chrome', count: 1, percent: 25, last_seen: '' },
      ],
    },
    {
      key: 'os',
      total: 4,
      distinct: 1,
      values: [{ value: 'Mac OS', count: 4, percent: 100, last_seen: '' }],
    },
  ],
});

describe('IssueDetailPage', () => {
  let http: HttpTestingController;

  beforeEach(() => {
    lang.set('es');
    TestBed.configureTestingModule({
      providers: [provideRouter([]), provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
  });

  afterEach(() => {
    // Cada cambio de estado vuelve a pedir el historial; aquí da igual su contenido.
    for (const req of http.match('/api/0/issues/5/history/')) {
      if (!req.cancelled) {
        req.flush({ data: [] });
      }
    }
    http.verify();
  });

  async function render(
    d: IssueDetail = detail(),
    tags: IssueTags = issueTags(),
    history: HistoryEntry[] = [],
  ) {
    const fixture = TestBed.createComponent(IssueDetailPage);
    fixture.componentRef.setInput('id', '5');
    fixture.detectChanges();
    http.expectOne('/api/0/issues/5/').flush(d);
    http.expectOne('/api/0/issues/5/tags/').flush(tags);
    http
      .expectOne((r) => r.url === '/api/0/issues/5/events/')
      .flush({
        data: [
          {
            id: 1,
            event_id: 'a'.repeat(32),
            occurred_at: d.last_seen,
            received_at: d.last_seen,
            release: 'fixture-release-1',
            environment: 'production',
            user: 'ana@example.com',
            message: '',
          },
          {
            id: 2,
            event_id: 'b'.repeat(32),
            occurred_at: d.first_seen,
            received_at: d.first_seen,
            release: 'fixture-release-0',
            environment: 'production',
            user: '',
            message: '',
          },
        ],
      });
    await fixture.whenStable();
    http.expectOne('/api/0/issues/5/history/').flush({ data: history });
    await fixture.whenStable();
    return fixture;
  }

  it('pinta el título, el estado, los datos, la gráfica y la excepción', async () => {
    const fixture = await render();
    const el: HTMLElement = fixture.nativeElement;
    expect(el.querySelector('.head__type')?.textContent).toBe('TypeError');
    expect(el.querySelector('.head .sp-badge')?.textContent).toBe('Abierto');
    expect(el.querySelector('.head__culprit')?.textContent).toContain('chunk-Z554BZQ6.js in a');
    const facts = Object.fromEntries(
      Array.from(el.querySelectorAll('.fact')).map((f) => [
        f.querySelector('dt')?.textContent,
        f.querySelector('dd')?.textContent,
      ]),
    );
    expect(facts['Proyecto']).toBe('front');
    expect(facts['Release']).toBe('fixture-release-1');
    expect(facts['Entorno']).toBe('production');
    expect(facts['Espacio']).toBe('42');
    expect(facts['Usuario']).toBe('ana@example.com');
    expect(facts['Request']).toBe('https://app.example.com/');
    expect(el.querySelectorAll('app-day-chart .col').length).toBe(14);
    expect(el.querySelector('.code__line--error .code__n')?.textContent).toBe('7');
    expect(el.querySelector('.libs__toggle')?.textContent).toContain('1 fotograma de librerías');
    expect(el.querySelector('app-breadcrumbs')?.textContent).toContain('navigation');
    expect(el.querySelectorAll('.tag').length).toBe(2);
    expect(el.querySelector('.pager__pos')?.textContent).toBe('1 de 2');
  });

  it('el panel de etiquetas da los porcentajes, busca al pulsar un valor y dice los usuarios', async () => {
    const fixture = await render();
    const el: HTMLElement = fixture.nativeElement;
    expect(el.querySelector('.users')?.textContent).toBe('3');
    expect(el.querySelector('.users__note')?.textContent).toContain('no guarda el dato en claro');
    const groups = Array.from(el.querySelectorAll('app-tags-panel .group'));
    // Navegador y sistema primero; el resto detrás.
    expect(groups.map((g) => g.querySelector('.group__key')?.textContent)).toEqual([
      'browser',
      'os',
      'release',
    ]);
    expect(groups[0].querySelector('.group__distinct')?.textContent).toBe('2 valores');
    const bars = Array.from(groups[0].querySelectorAll<HTMLAnchorElement>('a.bar'));
    expect(bars.map((b) => b.querySelector('.bar__value')?.textContent)).toEqual([
      'Safari',
      'Chrome',
    ]);
    expect(bars[0].querySelector('.bar__pct')?.textContent).toMatch(/^75\s?%$/);
    expect(bars[0].querySelector<HTMLElement>('.bar__fill')?.style.width).toBe('75%');
    expect(bars[0].getAttribute('title')).toBe('3 de 4 eventos');
    expect(bars[0].getAttribute('href')).toBe('/?query=is:open%20browser:Safari');
    const mac = groups[1].querySelector<HTMLAnchorElement>('a.bar');
    expect(mac?.getAttribute('href')).toBe('/?query=is:open%20os:%22Mac%20OS%22');
    expect(mac?.getAttribute('aria-label')).toMatch(/^Buscar errores con os:"Mac OS" · 100\s?%$/);
  });

  it('sin etiquetas aún, lo dice', async () => {
    const fixture = await render(detail({ user_count: 0 }), {
      issue_id: 5,
      events: 0,
      user_count: 0,
      tags: [],
    });
    const el: HTMLElement = fixture.nativeElement;
    expect(el.querySelector('.users')?.textContent).toBe('0');
    expect(el.querySelector('app-tags-panel')?.textContent).toContain(
      'Aún no hay etiquetas: se cuentan con cada evento nuevo.',
    );
  });

  it('resuelve con el botón principal y luego ofrece reabrir', async () => {
    const fixture = await render();
    const el: HTMLElement = fixture.nativeElement;
    const primary = el.querySelector<HTMLButtonElement>('.btn--primary');
    expect(primary?.textContent).toContain('Resolver');
    primary?.click();
    const req = http.expectOne('/api/0/issues/5/status');
    expect(req.request.body).toEqual({ status: 'resolved' });
    req.flush({ ...detail(), status: 'resolved' });
    await fixture.whenStable();
    expect(el.querySelector('.head .sp-badge')?.textContent).toBe('Resuelto');
    expect(el.querySelector('.btn--primary')?.textContent).toContain('Reabrir');
    expect(el.querySelector('.toast')?.textContent).toContain('resuelto');

    el.querySelector<HTMLButtonElement>('.btn--primary')?.click();
    const reopen = http.expectOne('/api/0/issues/5/status');
    expect(reopen.request.body).toEqual({ status: 'open' });
    reopen.flush({ ...detail(), status: 'open' });
    await fixture.whenStable();
    expect(el.querySelector('.head .sp-badge')?.textContent).toBe('Abierto');
  });

  it('el menú resuelve en la siguiente versión o silencia', async () => {
    const fixture = await render();
    const el: HTMLElement = fixture.nativeElement;
    el.querySelector<HTMLButtonElement>('[aria-label="Más acciones"]')?.click();
    fixture.detectChanges();
    const items = Array.from(el.querySelectorAll<HTMLButtonElement>('.more__item'));
    expect(items).toHaveLength(2);
    expect(items[0].textContent).toContain('Resolver en la siguiente versión');
    expect(items[0].textContent).toContain('fixture-release-1');
    expect(items[1].textContent).toContain('Silenciar');
    items[0].click();
    const req = http.expectOne('/api/0/issues/5/status');
    expect(req.request.body).toEqual({ status: 'resolved_next' });
    req.flush(
      { error: 'el último evento del issue no tiene release' },
      { status: 409, statusText: 'Conflict' },
    );
    await fixture.whenStable();
    expect(el.querySelector('.toast')?.textContent).toContain('no tiene release');
  });

  it('«Copiar para un agente» copia el context.md', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    const fixture = await render();
    const el: HTMLElement = fixture.nativeElement;
    const copy = Array.from(el.querySelectorAll<HTMLButtonElement>('.titlebar .btn')).find((b) =>
      b.textContent?.includes('Copiar para un agente'),
    );
    copy?.click();
    http.expectOne('/api/0/issues/5/context.md').flush('# TypeError: Cannot read properties');
    await new Promise((resolve) => setTimeout(resolve));
    await fixture.whenStable();
    expect(writeText).toHaveBeenCalledWith('# TypeError: Cannot read properties');
    expect(copy?.textContent).toContain('Copiado');
  });

  it('con ClipboardItem escribe dentro del clic, antes de tener el Markdown (Safari)', async () => {
    class FakeItem {
      constructor(readonly items: Record<string, Promise<Blob>>) {}
    }
    vi.stubGlobal('ClipboardItem', FakeItem);
    const write = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { write }, configurable: true });
    try {
      const fixture = await render();
      const el: HTMLElement = fixture.nativeElement;
      const copy = Array.from(el.querySelectorAll<HTMLButtonElement>('.titlebar .btn')).find((b) =>
        b.textContent?.includes('Copiar para un agente'),
      );
      copy?.click();
      // Aún sin respuesta del servidor: el portapapeles ya se ha pedido en el gesto.
      expect(write).toHaveBeenCalledOnce();
      const item = write.mock.calls[0][0][0] as FakeItem;
      http.expectOne('/api/0/issues/5/context.md').flush('# TypeError: md');
      const blob = await item.items['text/plain'];
      expect(await blob.text()).toBe('# TypeError: md');
      await new Promise((resolve) => setTimeout(resolve));
      await fixture.whenStable();
      expect(copy?.textContent).toContain('Copiado');
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it('si no llega el Markdown, dice el error de la API', async () => {
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: vi.fn() },
      configurable: true,
    });
    const fixture = await render();
    const el: HTMLElement = fixture.nativeElement;
    Array.from(el.querySelectorAll<HTMLButtonElement>('.titlebar .btn'))
      .find((b) => b.textContent?.includes('Copiar para un agente'))
      ?.click();
    http
      .expectOne('/api/0/issues/5/context.md')
      .flush({ error: 'no encontrado' }, { status: 404, statusText: 'x' });
    await new Promise((resolve) => setTimeout(resolve));
    await fixture.whenStable();
    expect(el.querySelector('.toast')?.textContent).toContain('no encontrado');
  });

  it('anterior y siguiente cambian de evento', async () => {
    const fixture = await render();
    const el: HTMLElement = fixture.nativeElement;
    const [prev, next] = Array.from(el.querySelectorAll<HTMLButtonElement>('.pager__btn'));
    expect(next.disabled).toBe(true);
    prev.click();
    http.expectOne('/api/0/events/2/').flush(stored(2, 'fixture-release-0', 250));
    await fixture.whenStable();
    expect(el.querySelector('.pager__pos')?.textContent).toBe('2 de 2');
    expect(el.querySelector('.code__line--error .code__n')?.textContent).toBe('250');
    expect(prev.disabled).toBe(true);
    expect(next.disabled).toBe(false);
  });

  it('un id que no existe dice que no existe', async () => {
    const fixture = TestBed.createComponent(IssueDetailPage);
    fixture.componentRef.setInput('id', '99');
    fixture.detectChanges();
    http
      .expectOne('/api/0/issues/99/')
      .flush({ error: 'no encontrado' }, { status: 404, statusText: 'Not Found' });
    http
      .expectOne((r) => r.url === '/api/0/issues/99/events/')
      .flush({ error: 'no encontrado' }, { status: 404, statusText: 'Not Found' });
    http
      .expectOne('/api/0/issues/99/tags/')
      .flush({ error: 'no encontrado' }, { status: 404, statusText: 'Not Found' });
    await fixture.whenStable();
    expect(fixture.nativeElement.querySelector('.state__title')?.textContent).toBe(
      'Este error no existe',
    );
  });

  it('con un entorno elegido pide el error y sus eventos de ese entorno, y enlaza sus releases', async () => {
    environment.set('staging');
    try {
      const fixture = TestBed.createComponent(IssueDetailPage);
      fixture.componentRef.setInput('id', '5');
      fixture.detectChanges();
      const issueReq = http.expectOne((r) => r.url === '/api/0/issues/5/');
      const eventsReq = http.expectOne((r) => r.url === '/api/0/issues/5/events/');
      expect(issueReq.request.params.get('environment')).toBe('staging');
      expect(eventsReq.request.params.get('environment')).toBe('staging');
      issueReq.flush(
        detail({ first_release: 'web@1.0.0', status: 'resolved', fixed_in_release: 'web@1.1.0' }),
      );
      eventsReq.flush({ data: [] });
      http.expectOne('/api/0/issues/5/tags/').flush(issueTags());
      await fixture.whenStable();
      const el: HTMLElement = fixture.nativeElement;
      const links = Array.from(
        el.querySelectorAll<HTMLAnchorElement>('.fact__link:not(.fact__link--settings)'),
      ).map((a) => [a.closest('.fact')?.querySelector('dt')?.textContent, a.getAttribute('href')]);
      expect(links).toEqual([
        ['Primera release', '/releases/4/web@1.0.0'],
        ['Última release', '/releases/4/fixture-release-1'],
        ['Resuelto con', '/releases/4/web@1.1.0'],
      ]);
    } finally {
      environment.set('');
    }
  });

  describe('en inglés', () => {
    const factsOf = (el: HTMLElement) =>
      Object.fromEntries(
        Array.from(el.querySelectorAll('.fact')).map((f) => [
          f.querySelector('dt')?.textContent,
          f.querySelector('dd')?.textContent,
        ]),
      );

    it('pinta la cabecera, los bloques, los datos y el menú en inglés', async () => {
      lang.set('en');
      const fixture = await render(detail({ last_regression: new Date().toISOString() }));
      const el: HTMLElement = fixture.nativeElement;
      expect(el.querySelector('.crumbs__root')?.textContent).toBe('Errors');
      expect(el.querySelector('[aria-label="Back to the list"]')).not.toBeNull();
      expect(el.querySelector('.head .sp-badge')?.textContent).toBe('Open');
      expect(el.querySelector('.head .sp-badge--yellow')?.textContent).toBe('Regression');
      expect(el.querySelector('.btn--primary')?.textContent?.trim()).toBe('Resolve');
      const titles = Array.from(el.querySelectorAll('.block__title')).map((h) =>
        h.childNodes[0]?.textContent?.trim(),
      );
      expect(titles).toEqual([
        'Last 14 days',
        'Event',
        'Exception',
        'Breadcrumbs',
        'Events',
        'History',
        'Details',
        'Affected users',
        'Tags',
        'Tags on this event',
      ]);
      expect(el.querySelector('.block__aside')?.textContent).toBe('2 events');
      expect(el.querySelector('.pager__pos')?.textContent).toBe('1 of 2');
      expect(el.querySelector('.libs__toggle')?.textContent).toContain('1 library frame');
      expect(el.querySelector('.users__note')?.textContent).toBe(
        'Distinct by id, email, username or IP. The count uses a hash and doesn’t store them in plain text.',
      );
      const firstBar = el.querySelector('app-tags-panel a.bar');
      expect(el.querySelector('.group__distinct')?.textContent).toBe('2 values');
      expect(firstBar?.getAttribute('title')).toBe('3 of 4 events');
      expect(firstBar?.getAttribute('aria-label')).toMatch(
        /^Search errors with browser:Safari · 75\s?%$/,
      );
      expect(el.querySelector('app-day-chart .axis')?.textContent).toContain('Today');
      const facts = factsOf(el);
      expect(Object.keys(facts)).toEqual([
        'Project',
        'First seen',
        'Last seen',
        'Regression',
        'Events',
        'Release',
        'Environment',
        'Workspace',
        'User',
        'Request',
        'Last release',
        'SDK',
      ]);
      expect(facts['First seen']).toBe('yesterday');
      expect(
        el.querySelector('.fact__link:not(.fact__link--settings)')?.getAttribute('href'),
        'la última release enlaza a su detalle',
      ).toBe('/releases/4/fixture-release-1');
      expect(facts['Environment']).toBe('production');

      el.querySelector<HTMLButtonElement>('[aria-label="More actions"]')?.click();
      fixture.detectChanges();
      const items = Array.from(el.querySelectorAll<HTMLButtonElement>('.more__item'));
      expect(items[0].textContent).toContain('Resolve in the next release');
      expect(items[0].textContent).toContain(
        'Resolved once a release later than fixture-release-1 is deployed; reopens if it happens again in that release or a later one.',
      );
      expect(items[1].textContent).toContain('Silence');
      expect(items[1].textContent).toContain('Keeps counting, but doesn’t alert.');
      expect(el.querySelector('[role=menu]')?.getAttribute('aria-label')).toBe('Change status');
      expect(spanishLeft(el)).toEqual([]);
    });

    it('el aviso al cambiar de estado y el botón de copiar, en inglés', async () => {
      lang.set('en');
      const writeText = vi.fn().mockResolvedValue(undefined);
      Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
      const fixture = await render();
      const el: HTMLElement = fixture.nativeElement;
      el.querySelector<HTMLButtonElement>('.btn--primary')?.click();
      http.expectOne('/api/0/issues/5/status').flush({ ...detail(), status: 'silenced' });
      await fixture.whenStable();
      expect(el.querySelector('.toast')?.textContent).toBe('Marked as resolved');
      expect(el.querySelector('.head .sp-badge')?.textContent).toBe('Silenced');
      expect(el.querySelector('.btn--primary')?.textContent?.trim()).toBe('Reopen');

      const copy = Array.from(el.querySelectorAll<HTMLButtonElement>('.titlebar .btn')).find((b) =>
        b.textContent?.includes('Copy for an agent'),
      );
      copy?.click();
      http.expectOne('/api/0/issues/5/context.md').flush('# md');
      await new Promise((resolve) => setTimeout(resolve));
      await fixture.whenStable();
      expect(copy?.textContent?.trim()).toBe('Copied');
      expect(el.querySelector('.toast')?.textContent).toBe('Copied: paste it into your agent');
    });

    it('un id que no existe, en inglés', async () => {
      lang.set('en');
      const fixture = TestBed.createComponent(IssueDetailPage);
      fixture.componentRef.setInput('id', 'x');
      fixture.detectChanges();
      await fixture.whenStable();
      const el: HTMLElement = fixture.nativeElement;
      expect(el.querySelector('.state__title')?.textContent).toBe('This error doesn’t exist');
      expect(el.querySelector('.state .btn')?.textContent).toBe('Go to the list');
      expect(spanishLeft(el)).toEqual([]);
    });

    it('cambia de idioma sin recargar el error', async () => {
      const fixture = await render();
      const el: HTMLElement = fixture.nativeElement;
      expect(factsOf(el)['Proyecto']).toBe('front');
      TestBed.inject(I18n).set('en');
      await fixture.whenStable();
      expect(factsOf(el)['Project']).toBe('front');
      expect(el.querySelector('.head .sp-badge')?.textContent).toBe('Open');
      expect(el.querySelector('.pager__pos')?.textContent).toBe('1 of 2');
      expect(el.querySelector('.group__distinct')?.textContent).toBe('2 values');
      expect(spanishLeft(el)).toEqual([]);
      TestBed.inject(I18n).set('es');
      await fixture.whenStable();
      expect(el.querySelector('.head .sp-badge')?.textContent).toBe('Abierto');
    });
  });
});
