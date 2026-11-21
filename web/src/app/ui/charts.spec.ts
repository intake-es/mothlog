import { TestBed } from '@angular/core/testing';
import type { DayCount } from '../core/api';
import { lang } from '../core/i18n';
import { spanishLeft } from '../core/i18n.testing';
import { DayChart } from './day-chart';
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
