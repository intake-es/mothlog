import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import type { CheckIn, MonitorDetail } from '../../core/api';
import { I18n, lang } from '../../core/i18n';
import { spanishLeft } from '../../core/i18n.testing';
import { MonitorDetailPage } from './monitor-detail';

const now = Date.now();
const iso = (msAgo: number) => new Date(now - msAgo).toISOString();
const MIN = 60_000;

const checkIn = (over: Partial<CheckIn>): CheckIn => ({
  id: 1,
  check_in_id: 'a'.repeat(32),
  status: 'ok',
  duration_ms: 201,
  environment: 'production',
  release: 'api@1.4.0',
  expected_at: iso(10 * MIN),
  started_at: iso(10 * MIN),
  finished_at: iso(10 * MIN),
  ...over,
});

const DETAIL: MonitorDetail = {
  id: 2,
  project_id: 4,
  project: 'api',
  slug: 'limpieza-nocturna',
  name: 'Limpieza nocturna',
  schedule: { type: 'crontab', value: '0 3 * * *' },
  timezone: 'Europe/Madrid',
  checkin_margin: 5,
  max_runtime: 20,
  environment: 'production',
  muted: false,
  status: 'timeout',
  last_checkin_at: iso(2 * MIN),
  next_checkin_at: new Date(now + 30 * MIN).toISOString(),
  created_at: iso(60 * MIN),
  updated_at: iso(60 * MIN),
  checkins: [
    checkIn({
      id: 3,
      status: 'timeout',
      duration_ms: null,
      started_at: iso(2 * MIN),
      finished_at: null,
    }),
    checkIn({
      id: 2,
      status: 'missed',
      duration_ms: null,
      environment: 'production',
      release: '',
      started_at: iso(5 * MIN),
    }),
    checkIn({ id: 1, duration_ms: 75_000 }),
  ],
  next_cursor: '1',
};

describe('MonitorDetailPage', () => {
  let http: HttpTestingController;

  async function render() {
    const fixture = TestBed.createComponent(MonitorDetailPage);
    fixture.componentRef.setInput('project', '4');
    fixture.componentRef.setInput('slug', 'limpieza-nocturna');
    fixture.detectChanges();
    http.expectOne('/api/0/projects/4/monitors/limpieza-nocturna/').flush(DETAIL);
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

  it('enseña el horario, el estado y el historial, y carga más', async () => {
    const { fixture, el } = await render();
    expect(el.querySelector('.head__name')?.textContent).toBe('Limpieza nocturna');
    expect(el.querySelector('.head .sp-badge')?.textContent).toBe('Se pasó de tiempo');
    const stats = Array.from(el.querySelectorAll('.stat')).map((s) =>
      Array.from(s.children)
        .map((c) => c.textContent?.trim())
        .join(' '),
    );
    expect(stats[0]).toBe('Horario 0 3 * * * Europe/Madrid');
    expect(stats[1]).toMatch(/^Próxima ejecución dentro de 30 min Margen de 5 minutos$/);
    expect(stats[2]).toBe('Último check-in hace 2 min Puede durar 20 minutos');
    const rows = Array.from(el.querySelectorAll('.history__row')).map((r) =>
      Array.from(r.querySelectorAll('[role=cell]')).map((c) => c.textContent?.trim()),
    );
    expect(rows[0].slice(0, 3)).toEqual(['Se pasó de tiempo', 'hace 2 min', '—']);
    expect(rows[1][0]).toBe('No llegó');
    expect(rows[1][5]).toBe('—');
    expect(rows[2].slice(0, 3)).toEqual(['Bien', 'hace 10 min', '1 min 15 s']);
    expect(rows[2][5]).toBe('api@1.4.0');

    el.querySelector<HTMLButtonElement>('.more .btn')?.click();
    const req = http.expectOne(
      (r) => r.url === '/api/0/projects/4/monitors/limpieza-nocturna/checkins/',
    );
    expect(req.request.params.get('cursor')).toBe('1');
    req.flush({ data: [checkIn({ id: 0, status: 'error', started_at: iso(70 * MIN) })] });
    await fixture.whenStable();
    fixture.detectChanges();
    expect(el.querySelectorAll('.history__row')).toHaveLength(4);
    expect(el.querySelector('.more')).toBeNull();
  });

  it('silencia el monitor con PATCH', async () => {
    const { fixture, el } = await render();
    const btn = el.querySelector<HTMLButtonElement>('.titlebar__actions .btn')!;
    expect(btn.textContent).toContain('Silenciar');
    btn.click();
    const req = http.expectOne('/api/0/projects/4/monitors/limpieza-nocturna/');
    expect(req.request.method).toBe('PATCH');
    expect(req.request.body).toEqual({ muted: true });
    const { checkins: _, next_cursor: __, ...plain } = DETAIL;
    req.flush({ ...plain, muted: true });
    await fixture.whenStable();
    fixture.detectChanges();
    expect(btn.textContent).toContain('Volver a avisar');
    expect(el.querySelectorAll('.history__row')).toHaveLength(3);
  });

  it('si no existe lo dice', async () => {
    const fixture = TestBed.createComponent(MonitorDetailPage);
    fixture.componentRef.setInput('project', '4');
    fixture.componentRef.setInput('slug', 'nadie');
    fixture.detectChanges();
    http
      .expectOne('/api/0/projects/4/monitors/nadie/')
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
    expect(el.querySelector('.head .sp-badge')?.textContent).toBe('Timed out');
    expect(el.textContent).toContain('May run for 20 minutes');
    expect(spanishLeft(el)).toEqual([]);
  });
});
