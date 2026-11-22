import { TestBed } from '@angular/core/testing';
import type { DayCount, ResponseTimes, UptimeCheck } from '../core/api';
import { lang } from '../core/i18n';
import { spanishLeft } from '../core/i18n.testing';
import { DayChart } from './day-chart';
import { ResponseChart } from './response-chart';
import { Sparkline } from './sparkline';

const days = (counts: number[]): DayCount[] =>
  counts.map((count, i) => ({ date: `2026-11-${String(6 + i).padStart(2, '0')}`, count }));

describe('Sparkline', () => {
  beforeEach(() => lang.set('es'));

  it('dibuja una barra por día, más alta donde hay más eventos', () => {
    const fixture = TestBed.createComponent(Sparkline);
    fixture.componentRef.setInput('days', days([0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 4, 2]));
    fixture.detectChanges();
    const svg: SVGElement = fixture.nativeElement.querySelector('svg');
    const rects = Array.from(svg.querySelectorAll('rect'));
    expect(rects.length).toBe(14);
    const h = rects.map((r) => Number(r.getAttribute('height')));
    expect(h[12]).toBeGreaterThan(h[13]);
    expect(h[13]).toBeGreaterThan(h[2]);
    expect(h[0]).toBe(1);
    expect(rects[0].classList.contains('empty')).toBe(true);
    expect(svg.getAttribute('aria-label')).toBe('7 eventos en los últimos 14 días');
  });

  it('sin serie no dibuja nada', () => {
    const fixture = TestBed.createComponent(Sparkline);
    fixture.componentRef.setInput('days', undefined);
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelectorAll('rect').length).toBe(0);
  });
});

describe('DayChart', () => {
  beforeEach(() => lang.set('es'));

  it('marca hoy y escala las barras al máximo', () => {
    const fixture = TestBed.createComponent(DayChart);
    fixture.componentRef.setInput('days', days([1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 4, 0]));
    fixture.detectChanges();
    const cols: HTMLElement[] = Array.from(fixture.nativeElement.querySelectorAll('.col'));
    expect(cols.length).toBe(14);
    expect(cols[13].classList.contains('col--today')).toBe(true);
    const bars = cols.map((c) => (c.querySelector('.bar') as HTMLElement).style.height);
    expect(bars[12]).toBe('100%');
    expect(bars[0]).toBe('25%');
    expect(bars[13]).toBe('6px');
    expect(cols[12].textContent).toContain('4 eventos');
    expect(fixture.nativeElement.querySelector('.axis').textContent).toContain('Hoy');
  });
});

describe('gráficas en inglés', () => {
  beforeEach(() => lang.set('en'));
  afterEach(() => lang.set('es'));

  it('la minigráfica y la de días, con singular, fechas y Today', () => {
    const spark = TestBed.createComponent(Sparkline);
    spark.componentRef.setInput('days', days([0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1]));
    spark.detectChanges();
    expect(spark.nativeElement.querySelector('svg').getAttribute('aria-label')).toBe(
      '1 event in the last 14 days',
    );

    const chart = TestBed.createComponent(DayChart);
    chart.componentRef.setInput('days', days([1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1200, 0]));
    chart.detectChanges();
    const el: HTMLElement = chart.nativeElement;
    const cols = el.querySelectorAll('.col');
    expect(cols[0].textContent).toBe('Nov 6 · 1 event');
    expect(cols[12].textContent).toBe('Nov 18 · 1,200 events');
    expect(cols[13].textContent).toBe('Today · 0 events');
    expect(el.querySelector('.chart')?.getAttribute('aria-label')).toBe(
      '1,201 events in the last 14 days',
    );
    expect(el.querySelector('.axis')?.textContent).toBe('Nov 6Today');
    expect(spanishLeft(el)).toEqual([]);
  });
});

describe('ResponseChart con pocas comprobaciones', () => {
  const now = Date.parse('2026-11-22T10:07:00Z');
  const ago = (min: number) => new Date(now - min * 60_000).toISOString();
  const checks: UptimeCheck[] = [
    { checked_at: ago(1), ok: true, status_code: 200, duration_ms: 120 },
    { checked_at: ago(2), ok: false, status_code: 503, duration_ms: 40, error: 'código 503' },
    { checked_at: ago(3), ok: true, status_code: 200, duration_ms: 180 },
    { checked_at: ago(4), ok: true, status_code: 200, duration_ms: 150 },
  ];
  const buckets: ResponseTimes[] = [
    { at: '2026-11-22T10:00:00Z', avg_ms: 150, max_ms: 180, checks: 4, failures: 1 },
  ];

  function render(opts: { checks?: UptimeCheck[]; since?: string; buckets?: ResponseTimes[] }) {
    const fixture = TestBed.createComponent(ResponseChart);
    fixture.componentRef.setInput('now', now);
    fixture.componentRef.setInput('buckets', opts.buckets ?? buckets);
    fixture.componentRef.setInput('checks', opts.checks ?? checks);
    fixture.componentRef.setInput('since', opts.since ?? ago(5));
    fixture.detectChanges();
    return fixture.nativeElement as HTMLElement;
  }

  afterEach(() => lang.set('es'));

  it('pinta cada comprobación desde la primera, no un tramo en el borde', () => {
    lang.set('es');
    const el = render({});
    const svg = el.querySelector('svg')!;
    expect(svg.getAttribute('aria-label')).toBe(
      'Tiempo de respuesta de cada comprobación de los últimos 4 min: 4, hasta 180 ms; fallidas: 1',
    );
    expect(svg.querySelectorAll('rect.hit')).toHaveLength(4);
    expect(svg.querySelectorAll('rect.fail')).toHaveLength(1);
    // La línea une las tres buenas y cada una lleva su punto.
    expect(svg.querySelectorAll('path.line')).toHaveLength(1);
    expect(svg.querySelectorAll('path.dot')).toHaveLength(3);
    // La primera queda a la izquierda, no apretada contra «Ahora».
    const xs = Array.from(svg.querySelectorAll('path.dot')).map((p) =>
      Number(/^M([\d.]+)/.exec(p.getAttribute('d') ?? '')?.[1]),
    );
    expect(xs[0]).toBeLessThan(300);
    expect(xs[2]).toBeGreaterThan(700);
    // Con margen arriba: la más lenta no toca el borde.
    expect(el.querySelector('.ymax')?.textContent).toBe('250 ms');
    expect(el.querySelector('.axis')?.textContent).toContain('Hace 4 min');
    expect(svg.querySelector('rect.hit title')?.textContent).toContain('150 ms · 200');
    expect(svg.querySelectorAll('rect.hit title')[2].textContent).toContain('Fallida: código 503');
  });

  it('en inglés', () => {
    lang.set('en');
    const el = render({});
    expect(el.querySelector('svg')?.getAttribute('aria-label')).toBe(
      'Response time of each check in the last 4 min: 4, up to 180 ms; failed: 1',
    );
    expect(el.querySelector('.axis')?.textContent).toContain('4 min ago');
    expect(el.querySelectorAll('rect.hit title')[2].textContent).toContain('Failed: código 503');
    expect(spanishLeft(el)).toEqual([]);
  });

  it('un monitor de unas horas empieza al crearlo, con al menos dos horas', () => {
    lang.set('es');
    const many = Array.from({ length: 12 }, (_, i) => ({
      at: new Date(Date.parse('2026-11-22T07:15:00Z') + i * 15 * 60_000).toISOString(),
      avg_ms: 100 + i,
      max_ms: 200,
      checks: 15,
      failures: 0,
    }));
    let el = render({ buckets: many, since: '2026-11-22T07:10:00Z' });
    // De 07:00 a 10:15: 13 tramos, no 96.
    expect(el.querySelectorAll('rect.hit')).toHaveLength(13);
    expect(el.querySelector('.axis')?.textContent).toContain('Hace 3 h 15 min');
    expect(el.querySelectorAll('path.line')).toHaveLength(1);

    el = render({ buckets: many.slice(-2), since: '2026-11-22T09:50:00Z' });
    expect(el.querySelectorAll('rect.hit')).toHaveLength(8);
    expect(el.querySelector('.axis')?.textContent).toContain('Hace 2 h');

    lang.set('en');
    el = render({ buckets: many, since: '2026-11-20T07:10:00Z' });
    expect(el.querySelectorAll('rect.hit')).toHaveLength(96);
    expect(el.querySelector('.axis')?.textContent).toContain('24 h ago');
    expect(spanishLeft(el)).toEqual([]);
  });
});
