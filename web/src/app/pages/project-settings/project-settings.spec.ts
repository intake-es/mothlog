import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { provideRouter, Router } from '@angular/router';
import type { DefaultAlerts, DroppedStats, Filters, Rule } from '../../core/api';
import { I18n, lang } from '../../core/i18n';
import { spanishLeft } from '../../core/i18n.testing';
import { ProjectSettingsPage } from './project-settings';

const PROJECTS = {
  data: [
    { id: 4, name: 'front', dsn: '' },
    { id: 7, name: 'backend', dsn: '' },
  ],
};

const rule = (over: Partial<Rule> = {}): Rule => ({
  id: 3,
  project_id: 4,
  name: 'Pagos en producción',
  enabled: true,
  conditions: {
    new: true,
    regressed: true,
    frequency: { events: 100, window_minutes: 60 },
    users: null,
    environments: ['production'],
    levels: [],
    tags: [{ key: 'area', value: 'pagos' }],
  },
  actions: [
    { id: 'hook', type: 'webhook', url: 'https://agente.example.com/avisos', secret: '…a1b2' },
    { id: 'slack', type: 'slack', url: 'https://hooks.slack.com/…wxyz' },
  ],
  throttle_minutes: 30,
  ...over,
});

const filters = (over: Partial<Filters> = {}): Filters => ({
  messages: ['^ResizeObserver loop'],
  exception_types: [],
  browser_extensions: true,
  web_crawlers: false,
  localhost: false,
  environments: ['development'],
  releases: [],
  events_per_issue_per_hour: null,
  sample_rate: 0.25,
  rate_limit_per_minute: 300,
  ...over,
});

const dropped: DroppedStats = {
  since: '2026-10-23',
  total: 1234,
  reasons: {
    message: 1000,
    exception_type: 0,
    browser_extension: 34,
    web_crawler: 0,
    localhost: 0,
    environment: 0,
    release: 0,
    rate_limit: 0,
    issue_limit: 200,
    sampled: 0,
  },
  daily: [],
};

const defaults = (over: Partial<DefaultAlerts> = {}): DefaultAlerts => ({
  slack: true,
  linear: true,
  configured: ['slack', 'linear'],
  has_rules: false,
  ...over,
});

describe('ProjectSettingsPage', () => {
  let http: HttpTestingController;

  beforeEach(() => {
    lang.set('es');
    TestBed.configureTestingModule({
      providers: [provideRouter([]), provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
  });

  afterEach(() => {
    // Cambiar de proyecto vuelve a pedir sus avisos por defecto.
    for (const req of http.match((r) => r.url.endsWith('/default-alerts'))) {
      if (!req.cancelled) {
        req.flush(defaults());
      }
    }
    http.verify();
    lang.set('es');
  });

  async function render(section: string, rules: Rule[] = [], d: DefaultAlerts = defaults()) {
    const fixture = TestBed.createComponent(ProjectSettingsPage);
    fixture.componentRef.setInput('id', '4');
    fixture.componentRef.setInput('section', section);
    fixture.detectChanges();
    http.expectOne('/api/0/projects/').flush(PROJECTS);
    await fixture.whenStable();
    fixture.detectChanges();
    if (section === 'alerts') {
      http.expectOne('/api/0/projects/4/default-alerts').flush(d);
      http.expectOne('/api/0/projects/4/rules').flush({ data: rules });
    } else {
      http.expectOne('/api/0/projects/4/filters').flush(filters());
      http.expectOne((r) => r.url === '/api/0/projects/4/dropped').flush(dropped);
    }
    await fixture.whenStable();
    fixture.detectChanges();
    return { fixture, el: fixture.nativeElement as HTMLElement };
  }

  it('sin reglas explica que avisa como el servidor y ofrece crear una', async () => {
    const { el } = await render('alerts');
    expect(el.querySelector('.titlebar__title')?.textContent).toContain('Ajustes');
    expect(el.querySelector('[aria-current=page]')?.textContent).toContain('Avisos');
    expect(el.querySelector('.note__title')?.textContent).toContain('Avisa como dice el servidor');
    expect(el.querySelector('.note__text')?.textContent).toContain('avisos por defecto de arriba');
    expect(el.querySelector('.btn--primary')?.textContent).toContain('Nueva regla');
  });

  it('los avisos por defecto tienen un interruptor por canal y guardan al cambiarlo', async () => {
    const { fixture, el } = await render('alerts');
    const section = el.querySelector('app-default-alerts')!;
    expect(section.querySelector('.section__title')?.textContent).toContain('Avisos por defecto');
    const rows = section.querySelectorAll('.defaults__row');
    expect(
      Array.from(rows).map((r) => r.querySelector('.defaults__name')?.textContent?.trim()),
    ).toEqual(['Slack', 'Linear']);
    expect(section.querySelector('.defaults__scope')?.textContent).toContain(
      'Solo valen mientras el proyecto no tenga reglas de errores',
    );
    const linear = section.querySelector<HTMLInputElement>('#default-linear')!;
    expect(linear.checked).toBe(true);
    linear.checked = false;
    linear.dispatchEvent(new Event('change'));
    const req = http.expectOne('/api/0/projects/4/default-alerts');
    expect(req.request.method).toBe('PATCH');
    expect(req.request.body).toEqual({ linear: false });
    req.flush(defaults({ linear: false }));
    await fixture.whenStable();
    fixture.detectChanges();
    expect(section.querySelector<HTMLInputElement>('#default-linear')?.checked).toBe(false);
    expect(el.querySelector('.toast')?.textContent).toContain('Linear apagado en este proyecto');
  });

  it('si guardar falla, el interruptor vuelve a como estaba', async () => {
    const { fixture, el } = await render('alerts');
    const slack = el.querySelector<HTMLInputElement>('#default-slack')!;
    slack.checked = false;
    slack.dispatchEvent(new Event('change'));
    http
      .expectOne('/api/0/projects/4/default-alerts')
      .flush({ error: 'no' }, { status: 500, statusText: 'Error' });
    await fixture.whenStable();
    fixture.detectChanges();
    expect(el.querySelector<HTMLInputElement>('#default-slack')?.checked).toBe(true);
  });

  it('un canal sin configurar en el servidor sale sin poder usarse', async () => {
    const { el } = await render('alerts', [], defaults({ configured: ['slack'] }));
    const linear = el.querySelector<HTMLInputElement>('#default-linear')!;
    expect(linear.disabled).toBe(true);
    expect(linear.checked).toBe(false);
    const row = linear.closest('.defaults__row')!;
    expect(row.textContent).toContain('Sin configurar');
    expect(row.textContent).toContain('El servidor no tiene LINEAR_API_KEY.');
  });

  it('con reglas de errores lo dice y lleva a ellas', async () => {
    const { el } = await render('alerts', [rule()], defaults({ has_rules: true }));
    const scope = el.querySelector('app-default-alerts .defaults__scope');
    expect(scope?.textContent).toContain('Este proyecto tiene reglas de errores');
    const rules = el.querySelector<HTMLElement>('#rules')!;
    rules.scrollIntoView = () => {};
    scope?.querySelector<HTMLButtonElement>('button')?.click();
    expect(document.activeElement).toBe(rules);
  });

  it('una regla solo de crons no cuenta como regla de errores', async () => {
    const cron = rule({
      conditions: {
        ...rule().conditions,
        new: false,
        regressed: false,
        frequency: null,
        cron: { failed: true, missed: true, timeout: false, recovered: false, monitors: [] },
      },
    });
    const { el } = await render('alerts', [cron]);
    expect(el.querySelector('app-default-alerts .defaults__scope')?.textContent).toContain(
      'Solo valen mientras',
    );
  });

  it('los avisos por defecto en inglés', async () => {
    lang.set('en');
    const { el } = await render('alerts', [], defaults({ configured: ['slack'] }));
    const section = el.querySelector('app-default-alerts') as HTMLElement;
    expect(section.querySelector('.section__title')?.textContent).toContain('Default alerts');
    expect(section.textContent).toContain('Not set up');
    expect(spanishLeft(section)).toEqual([]);
  });

  it('lista las reglas con su resumen y sus destinos', async () => {
    const { el } = await render('alerts', [
      rule(),
      rule({ id: 4, name: 'Apagada', enabled: false }),
    ]);
    const rows = el.querySelectorAll('.rule');
    expect(rows).toHaveLength(2);
    expect(rows[0].querySelector('.rule__name')?.textContent).toContain('Pagos en producción');
    expect(rows[0].querySelector('.rule__summary')?.textContent).toBe(
      'Error nuevo, regresión o más de 100 eventos en 1 h · en production · con area=pagos',
    );
    expect(rows[0].querySelectorAll('.rule__action')).toHaveLength(2);
    expect(rows[0].querySelector('.rule__every')?.textContent).toContain('30 min');
    expect(rows[1].querySelector('.sp-badge')?.textContent).toBe('Desactivada');
    expect(el.querySelector('.note')).toBeNull();
  });

  it('crea una regla con webhook y enseña el secreto generado una vez', async () => {
    const { fixture, el } = await render('alerts');
    el.querySelector<HTMLButtonElement>('.btn--primary')?.click();
    fixture.detectChanges();
    const name = el.querySelector<HTMLInputElement>('input[name=name]')!;
    name.value = 'Al agente';
    name.dispatchEvent(new Event('input'));
    const url = el.querySelector<HTMLInputElement>('.dest input')!;
    url.value = 'https://agente.example.com/avisos';
    url.dispatchEvent(new Event('input'));
    const env = el.querySelector<HTMLInputElement>('input[placeholder="production, staging"]')!;
    env.value = 'production, staging';
    env.dispatchEvent(new Event('input'));
    fixture.detectChanges();
    el.querySelector<HTMLButtonElement>('button[type=submit]')?.click();

    const req = http.expectOne('/api/0/projects/4/rules');
    expect(req.request.method).toBe('POST');
    expect(req.request.body).toMatchObject({
      name: 'Al agente',
      enabled: true,
      conditions: {
        new: true,
        regressed: true,
        frequency: null,
        users: null,
        environments: ['production', 'staging'],
      },
      actions: [{ type: 'webhook', url: 'https://agente.example.com/avisos', secret: '' }],
      throttle_minutes: 30,
    });
    const secret = 'f'.repeat(64);
    const actionID = req.request.body.actions[0].id;
    req.flush(
      rule({
        id: 9,
        name: 'Al agente',
        actions: [
          { id: actionID, type: 'webhook', url: 'https://agente.example.com/avisos', secret },
        ],
      }),
    );
    await fixture.whenStable();
    fixture.detectChanges();
    expect(el.querySelector('.secret__value')?.textContent).toBe(secret);
    expect(el.querySelector('.toast')?.textContent).toContain('Regla guardada');
    expect(el.querySelector('.rule--open .rule__name')?.textContent).toContain('Al agente');
    const secretInput = Array.from(
      el.querySelectorAll<HTMLInputElement>('.rule--open .dest input'),
    )[1];
    expect(secretInput.value).toBe('…ffff');
  });

  it('al marcar los usuarios explica de dónde salen, en los dos idiomas', async () => {
    const { fixture, el } = await render('alerts');
    el.querySelector<HTMLButtonElement>('.btn--primary')?.click();
    fixture.detectChanges();
    const users = el.querySelector<HTMLInputElement>('#cond-users')!;
    users.checked = true;
    users.dispatchEvent(new Event('change'));
    fixture.detectChanges();
    const hint = () =>
      users.closest('div')?.parentElement?.querySelector('.field__hint')?.textContent;
    expect(hint()).toBe(
      'Sin filtros, los usuarios afectados del error, también los de antes de crear la regla. Con filtros, los usuarios distintos de los eventos que los pasan, desde que existe la regla.',
    );
    expect(el.textContent).not.toContain('Provisional');
    TestBed.inject(I18n).set('en');
    fixture.detectChanges();
    expect(hint()).toBe(
      'Without filters, the error’s affected users, including those from before the rule existed. With filters, the distinct users of the events that pass them, since the rule exists.',
    );
  });

  it('crea una regla de crons para unos monitores y la resume en los dos idiomas', async () => {
    const { fixture, el } = await render('alerts');
    el.querySelector<HTMLButtonElement>('.btn--primary')?.click();
    fixture.detectChanges();
    const check = (label: string) =>
      Array.from(el.querySelectorAll<HTMLLabelElement>('label.check'))
        .find((l) => l.textContent?.includes(label))
        ?.querySelector('input');
    for (const label of ['Un error nuevo', 'Una regresión']) {
      check(label)?.click();
    }
    check('Un cron que falla')?.click();
    check('Un cron que no llega a su hora')?.click();
    check('Un cron que se pasa del tiempo máximo')?.click();
    const name = el.querySelector<HTMLInputElement>('input[name=name]')!;
    name.value = 'Crons';
    name.dispatchEvent(new Event('input'));
    const monitors = el.querySelector<HTMLInputElement>(
      'input[placeholder="copia-seguridad, enviar-facturas"]',
    )!;
    monitors.value = 'copia, facturas';
    monitors.dispatchEvent(new Event('input'));
    const url = el.querySelector<HTMLInputElement>('.dest input')!;
    url.value = 'https://agente.example.com/crons';
    url.dispatchEvent(new Event('input'));
    fixture.detectChanges();
    el.querySelector<HTMLButtonElement>('button[type=submit]')?.click();

    const req = http.expectOne('/api/0/projects/4/rules');
    expect(req.request.body.conditions).toMatchObject({
      new: false,
      regressed: false,
      cron: {
        failed: true,
        missed: true,
        timeout: true,
        recovered: false,
        monitors: ['copia', 'facturas'],
      },
    });
    const saved = rule({
      id: 9,
      name: 'Crons',
      conditions: { ...rule().conditions, ...req.request.body.conditions, tags: [] },
    });
    req.flush(saved);
    await fixture.whenStable();
    fixture.detectChanges();
    const summary = () =>
      Array.from(el.querySelectorAll('.rule__summary')).map((n) => n.textContent)[0];
    expect(summary()).toBe(
      'Cron que falla, cron que no llega o cron que se pasa de tiempo (copia o facturas)',
    );
    TestBed.inject(I18n).set('en');
    fixture.detectChanges();
    expect(summary()).toBe(
      'A cron fails, a cron is missed, or a cron times out (copia or facturas)',
    );
    const editor = el.querySelector('app-rule-editor')!;
    expect(editor.textContent).toContain('A cron runs past its maximum runtime');
    expect(spanishLeft(editor)).toEqual([]);
  });

  it('crea una regla de disponibilidad y la resume en los dos idiomas', async () => {
    const { fixture, el } = await render('alerts');
    el.querySelector<HTMLButtonElement>('.btn--primary')?.click();
    fixture.detectChanges();
    const check = (label: string) =>
      Array.from(el.querySelectorAll<HTMLLabelElement>('label.check'))
        .find((l) => l.textContent?.includes(label))
        ?.querySelector('input');
    for (const label of ['Un error nuevo', 'Una regresión']) {
      check(label)?.click();
    }
    check('Un monitor que cae')?.click();
    check('Un monitor que vuelve')?.click();
    const name = el.querySelector<HTMLInputElement>('input[name=name]')!;
    name.value = 'Web caída';
    name.dispatchEvent(new Event('input'));
    const monitors = el.querySelector<HTMLInputElement>('input[placeholder="web-principal, api"]')!;
    monitors.value = 'web-principal';
    monitors.dispatchEvent(new Event('input'));
    const url = el.querySelector<HTMLInputElement>('.dest input')!;
    url.value = 'https://agente.example.com/web';
    url.dispatchEvent(new Event('input'));
    fixture.detectChanges();
    el.querySelector<HTMLButtonElement>('button[type=submit]')?.click();

    const req = http.expectOne('/api/0/projects/4/rules');
    expect(req.request.body.conditions).toMatchObject({
      new: false,
      regressed: false,
      cron: null,
      uptime: { down: true, up: true, monitors: ['web-principal'] },
    });
    req.flush(
      rule({
        id: 9,
        name: 'Web caída',
        conditions: { ...rule().conditions, ...req.request.body.conditions, tags: [] },
      }),
    );
    await fixture.whenStable();
    fixture.detectChanges();
    const summary = () =>
      Array.from(el.querySelectorAll('.rule__summary')).map((n) => n.textContent)[0];
    expect(summary()).toBe('Monitor que cae o monitor que vuelve (web-principal)');
    TestBed.inject(I18n).set('en');
    fixture.detectChanges();
    expect(summary()).toBe('A monitor goes down or a monitor is back up (web-principal)');
    const editor = el.querySelector('app-rule-editor')!;
    expect(editor.textContent).toContain('A monitor is back up, with how long the incident lasted');
    expect(spanishLeft(editor)).toEqual([]);
  });

  it('el botón de prueba manda un aviso por esa acción y dice cómo ha ido', async () => {
    const { fixture, el } = await render('alerts', [rule()]);
    el.querySelector<HTMLButtonElement>('.rule__main')?.click();
    fixture.detectChanges();
    const tests = Array.from(el.querySelectorAll<HTMLButtonElement>('.dest .btn--small'));
    expect(tests.map((b) => b.textContent?.trim())).toEqual(['Enviar prueba', 'Enviar prueba']);
    tests[0].click();
    fixture.detectChanges();
    expect(tests[0].textContent).toContain('Enviando…');
    const req = http.expectOne('/api/0/projects/4/rules/3/test');
    expect(req.request.body).toEqual({ action_id: 'hook' });
    req.flush({ ok: true });
    await fixture.whenStable();
    fixture.detectChanges();
    expect(el.querySelector('.dest .ok')?.textContent).toBe('Prueba enviada');

    tests[1].click();
    http
      .expectOne('/api/0/projects/4/rules/3/test')
      .flush(
        { error: 'el aviso de prueba ha fallado: slack: 404 Not Found: no_service' },
        { status: 502, statusText: 'Bad Gateway' },
      );
    await fixture.whenStable();
    fixture.detectChanges();
    expect(el.querySelectorAll('.dest')[1].querySelector('.err')?.textContent).toContain(
      'no_service',
    );

    // Con cambios sin guardar no se prueba: la prueba usa la regla guardada.
    const name = el.querySelector<HTMLInputElement>('input[name=name]')!;
    name.value = 'Otro nombre';
    name.dispatchEvent(new Event('input'));
    fixture.detectChanges();
    expect(el.querySelector<HTMLButtonElement>('.dest .btn--small')?.disabled).toBe(true);
    expect(el.querySelector<HTMLButtonElement>('.dest .btn--small')?.title).toContain('Guarda');
  });

  it('al editar, los secretos tapados vuelven tal cual y se guardan con PUT', async () => {
    const { fixture, el } = await render('alerts', [rule()]);
    el.querySelector<HTMLButtonElement>('.rule__main')?.click();
    fixture.detectChanges();
    expect(el.textContent).toContain('Guardado. Déjalo así para conservarlo');
    const minutes = el.querySelector<HTMLInputElement>('[aria-label="Minutos entre avisos"]')!;
    minutes.value = '60';
    minutes.dispatchEvent(new Event('input'));
    fixture.detectChanges();
    el.querySelector<HTMLButtonElement>('button[type=submit]')?.click();
    const req = http.expectOne('/api/0/projects/4/rules/3');
    expect(req.request.method).toBe('PUT');
    expect(req.request.body.throttle_minutes).toBe(60);
    expect(req.request.body.actions[0].secret).toBe('…a1b2');
    expect(req.request.body.actions[1].url).toBe('https://hooks.slack.com/…wxyz');
    req.flush(rule({ throttle_minutes: 60 }));
    await fixture.whenStable();
  });

  it('borra una regla tras confirmarlo', async () => {
    const { fixture, el } = await render('alerts', [rule()]);
    el.querySelector<HTMLButtonElement>('.rule__main')?.click();
    fixture.detectChanges();
    const del = el.querySelector<HTMLButtonElement>('.danger')!;
    del.click();
    fixture.detectChanges();
    expect(del.textContent).toContain('Sí, borrarla');
    del.click();
    http
      .expectOne('/api/0/projects/4/rules/3')
      .flush(null, { status: 204, statusText: 'No Content' });
    await fixture.whenStable();
    fixture.detectChanges();
    expect(el.querySelectorAll('.rule')).toHaveLength(0);
    expect(el.querySelector('.note__title')?.textContent).toContain('Avisa como dice el servidor');
  });

  it('los filtros enseñan lo guardado, lo descartado y guardan con PATCH', async () => {
    const { fixture, el } = await render('filters');
    expect(el.querySelector('[aria-current=page]')?.textContent).toContain('Filtros');
    const areas = el.querySelectorAll<HTMLTextAreaElement>('textarea');
    expect(areas[0].value).toBe('^ResizeObserver loop');
    expect(el.querySelectorAll<HTMLInputElement>('.switch')[0].checked).toBe(true);
    const nums = el.querySelectorAll<HTMLInputElement>('input[type=number]');
    expect([nums[0].value, nums[1].value, nums[2].value]).toEqual(['', '25', '300']);
    expect(el.textContent).toContain('1234 eventos en los últimos 30 días');
    const drops = Array.from(el.querySelectorAll('.drop')).map((d) => [
      d.querySelector('.drop__label')?.textContent,
      d.querySelector('.drop__count')?.textContent,
    ]);
    expect(drops).toEqual([
      ['Mensaje', '1000'],
      ['Tope por error y hora', '200'],
      ['Extensión del navegador', '34'],
    ]);

    expect(el.querySelector<HTMLButtonElement>('button[type=submit]')?.disabled).toBe(true);
    nums[0].value = '20';
    nums[0].dispatchEvent(new Event('input'));
    nums[1].value = '50';
    nums[1].dispatchEvent(new Event('input'));
    areas[3].value = '1.*\n\n  2.0 ';
    areas[3].dispatchEvent(new Event('input'));
    fixture.detectChanges();
    expect(el.textContent).toContain('Hay cambios sin guardar');
    el.querySelector<HTMLButtonElement>('button[type=submit]')?.click();
    const req = http.expectOne('/api/0/projects/4/filters');
    expect(req.request.method).toBe('PATCH');
    expect(req.request.body).toEqual({
      ...filters(),
      events_per_issue_per_hour: 20,
      sample_rate: 0.5,
      releases: ['1.*', '2.0'],
    });
    req.flush(
      filters({ events_per_issue_per_hour: 20, sample_rate: 0.5, releases: ['1.*', '2.0'] }),
    );
    await fixture.whenStable();
    fixture.detectChanges();
    expect(el.querySelector('.toast')?.textContent).toContain('Filtros guardados');
  });

  it('tiene la sección Resumen entre las pestañas y la abre por su ruta', async () => {
    const { el } = await render('alerts');
    const tabs = Array.from(el.querySelectorAll<HTMLAnchorElement>('.tabs__tab'));
    const digest = tabs.find((a) => a.textContent?.includes('Resumen'));
    expect(digest?.getAttribute('href')).toBe('/projects/4/settings/digest');

    const fixture = TestBed.createComponent(ProjectSettingsPage);
    fixture.componentRef.setInput('id', '4');
    fixture.componentRef.setInput('section', 'digest');
    fixture.detectChanges();
    http.expectOne('/api/0/projects/').flush(PROJECTS);
    await fixture.whenStable();
    fixture.detectChanges();
    http.expectOne('/api/0/projects/4/digest');
    expect(fixture.nativeElement.querySelector('app-digest-settings')).not.toBeNull();
    expect(fixture.nativeElement.querySelector('[aria-current=page]')?.textContent).toContain(
      'Resumen',
    );
  });

  it('cambia de proyecto desde el selector', async () => {
    const { fixture, el } = await render('filters');
    const router = TestBed.inject(Router);
    const nav = vi.spyOn(router, 'navigate').mockResolvedValue(true);
    el.querySelector<HTMLButtonElement>('app-pick .pick')?.click();
    fixture.detectChanges();
    const items = Array.from(el.querySelectorAll<HTMLButtonElement>('.menu__item'));
    items.find((i) => i.textContent?.includes('backend'))?.click();
    expect(nav).toHaveBeenCalledWith(['/projects', 7, 'settings', 'filters'], {
      replaceUrl: false,
    });
  });

  it('en inglés no queda texto en español, en avisos ni en filtros', async () => {
    TestBed.inject(I18n).set('en');
    const alerts = await render('alerts', [rule({ name: 'Payments' })]);
    alerts.el.querySelector<HTMLButtonElement>('.rule__main')?.click();
    alerts.fixture.detectChanges();
    expect(alerts.el.querySelector('.rule__summary')?.textContent).toBe(
      'New error, regression, or more than 100 events in 1 hr · in production · with area=pagos',
    );
    expect(alerts.el.textContent).toContain('Send test');
    expect(spanishLeft(alerts.el)).toEqual([]);

    const f = await render('filters');
    expect(f.el.textContent).toContain('1,234 events in the last 30 days');
    expect(f.el.textContent).toContain('Per-error hourly cap');
    expect(spanishLeft(f.el)).toEqual([]);
  });
});
