import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { provideRouter, Router } from '@angular/router';
import type { UptimeMonitor } from '../../core/api';
import { I18n, lang } from '../../core/i18n';
import { spanishLeft } from '../../core/i18n.testing';
import { UptimeFormPage } from './uptime-form';

const MONITOR: UptimeMonitor = {
  id: 7,
  project_id: 5,
  project: 'web',
  slug: 'web-principal',
  name: 'Web principal',
  url: 'https://example.com/',
  method: 'GET',
  interval_seconds: 60,
  timeout_seconds: 10,
  expected_status: '200-299',
  failure_threshold: 2,
  paused: false,
  status: 'up',
  consecutive_failures: 0,
  last_checked_at: null,
  last_status_code: null,
  last_duration_ms: null,
  last_error: '',
  next_check_at: new Date().toISOString(),
  created_at: new Date().toISOString(),
  updated_at: new Date().toISOString(),
  down_since: null,
  uptime_24h: null,
  uptime_30d: null,
};

describe('UptimeFormPage', () => {
  let http: HttpTestingController;
  let navigated: unknown[][];

  async function render(inputs: Record<string, string>) {
    const fixture = TestBed.createComponent(UptimeFormPage);
    for (const [k, v] of Object.entries(inputs)) {
      fixture.componentRef.setInput(k, v);
    }
    fixture.detectChanges();
    return { fixture, el: fixture.nativeElement as HTMLElement };
  }

  function type(el: HTMLElement, selector: string, value: string) {
    const input = el.querySelector<HTMLInputElement | HTMLSelectElement>(selector)!;
    input.value = value;
    input.dispatchEvent(new Event(input instanceof HTMLSelectElement ? 'change' : 'input'));
  }

  beforeEach(() => {
    lang.set('es');
    TestBed.configureTestingModule({
      providers: [provideRouter([]), provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
    navigated = [];
    const router = TestBed.inject(Router);
    router.navigate = (commands: unknown[]) => {
      navigated.push(commands);
      return Promise.resolve(true);
    };
  });

  afterEach(() => {
    http.verify();
    lang.set('es');
  });

  it('crea un monitor en el proyecto de la URL y lleva a su detalle', async () => {
    const { fixture, el } = await render({ project: 'web' });
    http.expectOne('/api/0/projects/').flush({
      data: [
        { id: 4, name: 'api', dsn: '' },
        { id: 5, name: 'web', dsn: '' },
      ],
    });
    await fixture.whenStable();
    fixture.detectChanges();
    expect(el.querySelector('.crumbs__leaf')?.textContent).toBe('Nuevo monitor de disponibilidad');
    expect(el.querySelector<HTMLSelectElement>('select[name=project]')?.value).toBe('5');
    const submit = el.querySelector<HTMLButtonElement>('button[type=submit]')!;
    expect(submit.disabled).toBe(true);

    type(el, 'input[name=name]', 'Web principal');
    type(el, 'input[name=url]', ' https://example.com/ ');
    type(el, 'select[name=interval]', '30');
    type(el, 'input[name=timeout]', '5');
    type(el, 'input[name=expected]', '2xx,301');
    type(el, 'input[name=threshold]', '3');
    Array.from(el.querySelectorAll<HTMLButtonElement>('.seg__opt'))
      .find((b) => b.textContent === 'HEAD')
      ?.click();
    fixture.detectChanges();
    expect(submit.disabled).toBe(false);
    expect(submit.textContent?.trim()).toBe('Crear monitor');
    submit.click();

    const req = http.expectOne('/api/0/projects/5/uptime-monitors/');
    expect(req.request.method).toBe('POST');
    expect(req.request.body).toEqual({
      name: 'Web principal',
      url: 'https://example.com/',
      method: 'HEAD',
      interval_seconds: 30,
      timeout_seconds: 5,
      expected_status: '2xx,301',
      failure_threshold: 3,
      paused: false,
    });
    req.flush(MONITOR);
    await fixture.whenStable();
    expect(navigated).toEqual([['/monitors', 5, 'uptime', 'web-principal']]);
  });

  it('enseña el error de la API, como una URL de metadatos', async () => {
    const { fixture, el } = await render({});
    http.expectOne('/api/0/projects/').flush({ data: [{ id: 4, name: 'api', dsn: '' }] });
    await fixture.whenStable();
    fixture.detectChanges();
    type(el, 'input[name=name]', 'meta');
    type(el, 'input[name=url]', 'http://169.254.169.254/latest/meta-data/');
    fixture.detectChanges();
    el.querySelector<HTMLButtonElement>('button[type=submit]')?.click();
    http.expectOne('/api/0/projects/4/uptime-monitors/').flush(
      {
        error:
          'datos no válidos: la URL apunta a 169.254.169.254, una dirección de metadatos, link-local o reservada',
      },
      { status: 400, statusText: 'Bad Request' },
    );
    await fixture.whenStable();
    fixture.detectChanges();
    expect(el.querySelector('.err')?.textContent).toContain('169.254.169.254');
    expect(navigated).toEqual([]);
  });

  it('edita uno, lo pausa y lo borra tras confirmarlo', async () => {
    const { fixture, el } = await render({ project: '5', slug: 'web-principal' });
    http.expectOne('/api/0/projects/5/uptime-monitors/web-principal/').flush(MONITOR);
    await fixture.whenStable();
    fixture.detectChanges();
    expect(el.querySelector('.crumbs__leaf')?.textContent).toBe('Editar Web principal');
    expect(el.querySelector('select[name=project]')).toBeNull();
    expect(el.querySelector<HTMLInputElement>('input[name=url]')?.value).toBe(
      'https://example.com/',
    );
    el.querySelector<HTMLInputElement>('input.switch')?.click();
    fixture.detectChanges();
    el.querySelector<HTMLButtonElement>('button[type=submit]')?.click();
    const req = http.expectOne('/api/0/projects/5/uptime-monitors/web-principal/');
    expect(req.request.method).toBe('PATCH');
    expect(req.request.body.paused).toBe(true);
    req.flush({ ...MONITOR, paused: true });
    await fixture.whenStable();

    const del = el.querySelector<HTMLButtonElement>('.danger')!;
    del.click();
    fixture.detectChanges();
    expect(del.textContent?.trim()).toBe('Sí, borrarlo');
    del.click();
    const delReq = http.expectOne('/api/0/projects/5/uptime-monitors/web-principal/');
    expect(delReq.request.method).toBe('DELETE');
    delReq.flush(null, { status: 204, statusText: 'No Content' });
    await fixture.whenStable();
    expect(navigated.at(-1)).toEqual(['/monitors']);
  });

  it('en inglés no queda texto en español', async () => {
    TestBed.inject(I18n).set('en');
    const { fixture, el } = await render({});
    http.expectOne('/api/0/projects/').flush({ data: [{ id: 4, name: 'api', dsn: '' }] });
    await fixture.whenStable();
    fixture.detectChanges();
    expect(el.querySelector('.crumbs__leaf')?.textContent).toBe('New uptime monitor');
    expect(el.textContent).toContain('Failures in a row before it’s down');
    expect(el.querySelector('select[name=interval]')?.textContent).toContain('Every 30 s');
    expect(spanishLeft(el)).toEqual([]);
  });
  function pick(el: HTMLElement, name: string, option: string) {
    const host = el.querySelector(`app-pick:has(select[name=${name}])`)!;
    host.querySelector<HTMLButtonElement>('.pick')!.click();
    return () =>
      Array.from(host.querySelectorAll<HTMLButtonElement>('.menu__item'))
        .find((b) => b.textContent?.trim() === option)
        ?.click();
  }

  it('el proyecto y la frecuencia se eligen con los selectores del panel', async () => {
    const { fixture, el } = await render({});
    http.expectOne('/api/0/projects/').flush({
      data: [
        { id: 4, name: 'api', dsn: '' },
        { id: 5, name: 'web', dsn: '' },
      ],
    });
    await fixture.whenStable();
    fixture.detectChanges();
    expect(el.querySelector('select:not(.visually-hidden)')).toBeNull();
    expect(el.querySelector('.section__title')?.textContent).toBe('Comprobar una web o una API');
    const project = el.querySelector('app-pick:has(select[name=project]) .pick');
    expect(project?.getAttribute('aria-label')).toBe('Proyecto: api');

    const chooseWeb = pick(el, 'project', 'web');
    fixture.detectChanges();
    chooseWeb();
    fixture.detectChanges();
    expect(project?.getAttribute('aria-label')).toBe('Proyecto: web');

    const chooseInterval = pick(el, 'interval', 'Cada 5 minutos');
    fixture.detectChanges();
    chooseInterval();
    fixture.detectChanges();
    expect(el.querySelector('app-pick:has(select[name=interval]) .pick__value')?.textContent).toBe(
      'Cada 5 minutos',
    );

    type(el, 'input[name=name]', 'Web');
    type(el, 'input[name=url]', 'https://example.com/');
    fixture.detectChanges();
    el.querySelector<HTMLButtonElement>('button[type=submit]')?.click();
    const req = http.expectOne('/api/0/projects/5/uptime-monitors/');
    expect(req.request.body.interval_seconds).toBe(300);
    req.flush(MONITOR);
    await fixture.whenStable();
  });

  it('en inglés, los selectores y la cabecera del editor van traducidos', async () => {
    TestBed.inject(I18n).set('en');
    const { fixture, el } = await render({ project: '5', slug: 'web-principal' });
    http.expectOne('/api/0/projects/5/uptime-monitors/web-principal/').flush(MONITOR);
    await fixture.whenStable();
    fixture.detectChanges();
    expect(el.querySelector('.section__title')?.textContent).toBe('Web principal');
    const interval = el.querySelector('app-pick:has(select[name=interval]) .pick');
    expect(interval?.getAttribute('aria-label')).toBe('How often: Every minute');
    interval?.dispatchEvent(new MouseEvent('click'));
    fixture.detectChanges();
    expect(el.querySelector('.menu')?.textContent).toContain('Every 30 s');
    expect(spanishLeft(el)).toEqual([]);
  });
});
