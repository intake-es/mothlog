import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import type { HistoryEntry } from '../../core/api';
import { lang } from '../../core/i18n';
import { spanishLeft } from '../../core/i18n.testing';
import { History } from './history';

const minutesAgo = (n: number) => new Date(Date.now() - n * 60_000).toISOString();

const entries = (): HistoryEntry[] => [
  {
    type: 'comment',
    id: 1,
    at: minutesAgo(50),
    author: { name: 'ana', kind: 'user' },
    body: 'Pasa con pedidos sin líneas.\nLo miro.',
    mine: true,
  },
  {
    type: 'status',
    id: 1,
    at: minutesAgo(40),
    author: { name: 'agente', kind: 'token' },
    source: 'mcp',
    from: 'open',
    to: 'resolved_next',
    release: 'web@1.4.0',
    comment: {
      id: 2,
      issue_id: 5,
      author: { name: 'agente', kind: 'token' },
      body: 'Arreglado en `src/carrito.ts`',
      created_at: minutesAgo(40),
      mine: false,
    },
  },
  {
    type: 'status',
    id: 2,
    at: minutesAgo(30),
    author: null,
    source: 'deploy',
    from: 'resolved_next',
    to: 'resolved',
    release: 'web@1.5.0',
  },
  {
    type: 'status',
    id: 3,
    at: minutesAgo(20),
    author: null,
    source: 'ingest',
    from: 'resolved',
    to: 'open',
    release: 'web@1.6.0',
  },
];

describe('History', () => {
  let http: HttpTestingController;

  beforeEach(() => {
    lang.set('es');
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
  });

  afterEach(() => {
    http.verify();
    lang.set('es');
  });

  async function render(list: HistoryEntry[] = entries()) {
    const fixture = TestBed.createComponent(History);
    fixture.componentRef.setInput('issueId', 5);
    fixture.detectChanges();
    http.expectOne('/api/0/issues/5/history/').flush({ data: list });
    await fixture.whenStable();
    return fixture;
  }

  const lines = (el: HTMLElement) =>
    Array.from(el.querySelectorAll('.item__line')).map((l) =>
      (l.textContent ?? '').replace(/\s+/g, ' ').trim(),
    );

  it('pinta los comentarios y los cambios de estado, del más viejo al más nuevo', async () => {
    const fixture = await render();
    const el: HTMLElement = fixture.nativeElement;
    const ls = lines(el);
    expect(ls[0]).toMatch(/^ana comentó hace 50 min/);
    expect(ls[1]).toMatch(
      /^agente token cambió el estado Abierto → Resuelto en la siguiente versión web@1\.4\.0 desde el MCP hace 40 min/,
    );
    expect(ls[2]).toMatch(/^intake-errors lo resolvió al desplegar web@1\.5\.0 hace 30 min/);
    expect(ls[3]).toMatch(/^intake-errors lo reabrió al llegar un evento de web@1\.6\.0/);
    const bodies = Array.from(el.querySelectorAll('.comment__body')).map((b) => b.textContent);
    expect(bodies).toEqual([
      'Pasa con pedidos sin líneas.\nLo miro.',
      'Arreglado en `src/carrito.ts`',
    ]);
    expect(el.querySelector('.avatar')?.textContent?.trim()).toBe('A');
    // Solo se puede borrar lo propio.
    expect(el.querySelectorAll('.comment__delete').length).toBe(1);
    expect(el.querySelector('.comment__delete')?.getAttribute('aria-label')).toBe(
      'Borrar el comentario',
    );
  });

  it('sin nada, lo dice y deja comentar', async () => {
    const fixture = await render([]);
    const el: HTMLElement = fixture.nativeElement;
    expect(el.querySelector('.muted')?.textContent).toContain(
      'Aún no hay comentarios ni cambios de estado.',
    );
    const area = el.querySelector<HTMLTextAreaElement>('textarea');
    expect(area?.getAttribute('placeholder')).toBe(
      'Deja un comentario: qué has visto, qué se ha cambiado…',
    );
    expect(el.querySelector<HTMLButtonElement>('.composer .btn')?.disabled).toBe(true);
  });

  it('comenta con el botón y con Ctrl+Intro', async () => {
    const fixture = await render([]);
    const el: HTMLElement = fixture.nativeElement;
    const area = el.querySelector<HTMLTextAreaElement>('textarea') as HTMLTextAreaElement;
    area.value = '  La causa es el carrito vacío  ';
    area.dispatchEvent(new Event('input'));
    fixture.detectChanges();
    el.querySelector<HTMLButtonElement>('.composer .btn')?.click();
    const req = http.expectOne('/api/0/issues/5/comments/');
    expect(req.request.method).toBe('POST');
    expect(req.request.body).toEqual({ body: 'La causa es el carrito vacío' });
    req.flush({
      id: 9,
      issue_id: 5,
      author: { name: 'dev', kind: 'user' },
      body: 'La causa es el carrito vacío',
      created_at: new Date().toISOString(),
      mine: true,
    });
    await fixture.whenStable();
    expect(lines(el)[0]).toMatch(/^dev comentó/);
    expect(area.value).toBe('');

    area.value = 'Otro';
    area.dispatchEvent(new Event('input'));
    area.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', ctrlKey: true }));
    http
      .expectOne('/api/0/issues/5/comments/')
      .flush(
        { error: 'el comentario pasa de 20000 caracteres' },
        { status: 400, statusText: 'Bad Request' },
      );
    await fixture.whenStable();
    expect(area.value).toBe('Otro');
    expect(el.querySelectorAll('.item').length).toBe(1);
  });

  it('borra un comentario propio; el de un cambio deja el cambio', async () => {
    const list = entries();
    list[1].comment = { ...(list[1].comment as NonNullable<HistoryEntry['comment']>), mine: true };
    const fixture = await render(list);
    const el: HTMLElement = fixture.nativeElement;
    const buttons = el.querySelectorAll<HTMLButtonElement>('.comment__delete');
    expect(buttons.length).toBe(2);
    buttons[1].click();
    const req = http.expectOne('/api/0/issues/5/comments/2');
    expect(req.request.method).toBe('DELETE');
    req.flush(null, { status: 204, statusText: 'No Content' });
    await fixture.whenStable();
    expect(el.querySelectorAll('.item').length).toBe(4);
    expect(el.querySelectorAll('.comment').length).toBe(1);

    el.querySelector<HTMLButtonElement>('.comment__delete')?.click();
    http.expectOne('/api/0/issues/5/comments/1').flush(null, { status: 204, statusText: 'x' });
    await fixture.whenStable();
    expect(el.querySelectorAll('.item').length).toBe(3);
  });

  it('vuelve a pedir el historial cuando cambia la versión', async () => {
    const fixture = await render([]);
    fixture.componentRef.setInput('version', 1);
    fixture.detectChanges();
    http.expectOne('/api/0/issues/5/history/').flush({ data: entries() });
    await fixture.whenStable();
    expect(fixture.nativeElement.querySelectorAll('.item').length).toBe(4);
  });

  it('si falla, lo dice y deja reintentar', async () => {
    const fixture = TestBed.createComponent(History);
    fixture.componentRef.setInput('issueId', 5);
    fixture.detectChanges();
    http
      .expectOne('/api/0/issues/5/history/')
      .flush({ error: 'x' }, { status: 500, statusText: 'x' });
    await fixture.whenStable();
    const el: HTMLElement = fixture.nativeElement;
    expect(el.querySelector('[role=alert]')?.textContent).toContain(
      'No se ha podido cargar el historial.',
    );
    el.querySelector<HTMLButtonElement>('.link')?.click();
    http.expectOne('/api/0/issues/5/history/').flush({ data: [] });
    await fixture.whenStable();
    expect(el.querySelector('[role=alert]')).toBeNull();
  });

  const assignments = (): HistoryEntry[] => [
    {
      type: 'assignment',
      id: 1,
      at: minutesAgo(30),
      author: { name: 'luis', kind: 'user' },
      source: 'ui',
      assignee: { name: 'ana', kind: 'user' },
    },
    {
      type: 'assignment',
      id: 2,
      at: minutesAgo(20),
      author: { name: 'agente', kind: 'token' },
      source: 'mcp',
      previous: { name: 'ana', kind: 'user' },
      assignee: { name: 'agente', kind: 'token' },
    },
    {
      type: 'assignment',
      id: 3,
      at: minutesAgo(10),
      author: { name: 'luis', kind: 'user' },
      source: 'ui',
      previous: { name: 'agente', kind: 'token' },
    },
  ];

  it('pinta las asignaciones con quién las hizo', async () => {
    const fixture = await render(assignments());
    const el: HTMLElement = fixture.nativeElement;
    expect(lines(el)).toEqual([
      'luis lo asignó a ana desde la interfaz hace 30 min',
      'agente token se lo asignó desde el MCP hace 20 min',
      'luis se lo quitó a agente desde la interfaz hace 10 min',
    ]);
    expect(el.querySelectorAll('.marker svg').length).toBe(3);
  });

  describe('en inglés', () => {
    it('las asignaciones, en inglés', async () => {
      lang.set('en');
      const fixture = await render(assignments());
      const el: HTMLElement = fixture.nativeElement;
      expect(lines(el)).toEqual([
        'luis assigned it to ana from the app 30m ago',
        'agente token took it from MCP 20m ago',
        'luis unassigned agente from the app 10m ago',
      ]);
      expect(spanishLeft(el)).toEqual([]);
    });

    it('pinta el historial y el cuadro de comentar en inglés', async () => {
      lang.set('en');
      // Los comentarios son de quien los escribe: aquí, en inglés, para no confundir a spanishLeft.
      const list = entries().map((e) =>
        e.type === 'comment'
          ? { ...e, body: 'Happens with empty orders.' }
          : e.comment
            ? { ...e, comment: { ...e.comment, body: 'Fixed in `src/cart.ts`' } }
            : e,
      );
      const fixture = await render(list);
      const el: HTMLElement = fixture.nativeElement;
      const ls = lines(el);
      expect(ls[0]).toMatch(/^ana commented 50m ago/);
      expect(ls[1]).toMatch(
        /^agente token changed the status Open → Resolved in the next release web@1\.4\.0 from MCP/,
      );
      expect(ls[2]).toMatch(/^intake-errors resolved it when web@1\.5\.0 was deployed/);
      expect(ls[3]).toMatch(/^intake-errors reopened it when an event from web@1\.6\.0 came in/);
      expect(el.querySelector('.composer__hint')?.textContent).toBe('Ctrl or ⌘ + Enter to send');
      expect(el.querySelector('textarea')?.getAttribute('placeholder')).toBe(
        'Leave a comment: what you found, what changed…',
      );
      expect(el.querySelector('.composer .btn')?.textContent?.trim()).toBe('Comment');
      expect(el.querySelector('label')?.textContent).toBe('Comment');
      expect(el.querySelector('.comment__delete')?.getAttribute('aria-label')).toBe(
        'Delete comment',
      );
      expect(spanishLeft(el)).toEqual([]);
    });

    it('sin nada, en inglés', async () => {
      lang.set('en');
      const fixture = await render([]);
      const el: HTMLElement = fixture.nativeElement;
      expect(el.querySelector('.muted')?.textContent).toContain(
        'No comments or status changes yet.',
      );
      expect(spanishLeft(el)).toEqual([]);
    });
  });
});
