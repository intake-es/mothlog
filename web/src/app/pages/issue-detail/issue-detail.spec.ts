import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import type { IssueDetail, StoredEvent } from '../../core/api';
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

describe('IssueDetailPage', () => {
  let http: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [provideRouter([]), provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
  });

  afterEach(() => http.verify());

  async function render(d: IssueDetail = detail()) {
    const fixture = TestBed.createComponent(IssueDetailPage);
    fixture.componentRef.setInput('id', '5');
    fixture.detectChanges();
    http.expectOne('/api/0/issues/5/').flush(d);
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

  it('«Copiar para Claude» copia el stacktrace.md', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    const fixture = await render();
    const el: HTMLElement = fixture.nativeElement;
    const copy = Array.from(el.querySelectorAll<HTMLButtonElement>('.titlebar .btn')).find((b) =>
      b.textContent?.includes('Copiar para Claude'),
    );
    copy?.click();
    http.expectOne('/api/0/issues/5/stacktrace.md').flush('# TypeError: Cannot read properties');
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
        b.textContent?.includes('Copiar para Claude'),
      );
      copy?.click();
      // Aún sin respuesta del servidor: el portapapeles ya se ha pedido en el gesto.
      expect(write).toHaveBeenCalledOnce();
      const item = write.mock.calls[0][0][0] as FakeItem;
      http.expectOne('/api/0/issues/5/stacktrace.md').flush('# TypeError: md');
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
      .find((b) => b.textContent?.includes('Copiar para Claude'))
      ?.click();
    http
      .expectOne('/api/0/issues/5/stacktrace.md')
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
    await fixture.whenStable();
    expect(fixture.nativeElement.querySelector('.state__title')?.textContent).toBe(
      'Este error no existe',
    );
  });
});
