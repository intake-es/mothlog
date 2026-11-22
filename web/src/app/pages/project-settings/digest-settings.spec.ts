import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import type { DigestConfig, DigestPreview } from '../../core/api';
import { I18n, lang } from '../../core/i18n';
import { spanishLeft } from '../../core/i18n.testing';
import { DigestSettings } from './digest-settings';

const saved = (over: Partial<DigestConfig> = {}): DigestConfig => ({
  project_id: 4,
  enabled: true,
  frequency: 'daily',
  weekday: 1,
  hour: 9,
  timezone: 'Europe/Madrid',
  language: 'es',
  send_empty: true,
  destinations: [
    { id: 'correo', type: 'email', to: ['equipo@example.com'] },
    { id: 'hook', type: 'webhook', url: 'https://agente.example.com/resumen', secret: '…a1b2' },
  ],
  last_period_end: '2026-11-22T08:00:00Z',
  last_sent_at: '2026-11-22T08:00:12Z',
  next_send_at: '2026-11-23T08:00:00Z',
  ...over,
});

const preview = (subject: string): DigestPreview => ({
  subject,
  html: `<html><body><h1>${subject}</h1></body></html>`,
  text: subject,
  markdown: `# ${subject}`,
  digest: { empty: false, period: '', sections: [] },
});

describe('DigestSettings', () => {
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

  const previewReq = (language: string, frequency: string) =>
    http.expectOne(
      (r) =>
        r.url === '/api/0/projects/4/digest/preview' &&
        r.params.get('language') === language &&
        r.params.get('frequency') === frequency,
    );

  async function render(config = saved()) {
    const fixture = TestBed.createComponent(DigestSettings);
    fixture.componentRef.setInput('projectId', 4);
    fixture.detectChanges();
    http.expectOne('/api/0/projects/4/digest').flush(config);
    await fixture.whenStable();
    fixture.detectChanges();
    previewReq(config.language, config.frequency).flush(
      preview(
        config.language === 'es'
          ? 'Resumen diario de tienda: 1 nuevo'
          : 'Daily digest for tienda: 1 new',
      ),
    );
    await fixture.whenStable();
    fixture.detectChanges();
    return { fixture, el: fixture.nativeElement as HTMLElement };
  }

  const pickValue = (el: HTMLElement, label: string) =>
    el.querySelector(`.pick[aria-label^="${label}"] .pick__value`)?.textContent?.trim();

  it('enseña lo guardado, cuándo sale el próximo y la vista previa del correo', async () => {
    const { el } = await render();
    expect(el.querySelector('.section__title')?.textContent).toContain('Resumen del proyecto');
    expect(el.querySelector<HTMLInputElement>('input[name=enabled]')?.checked).toBe(true);
    expect(el.querySelector('.status')?.textContent).toMatch(/El próximo sale el .*23.*nov.*09:00/);
    expect(el.querySelector('.status')?.textContent).toContain('El último salió el');
    expect(pickValue(el, 'Frecuencia')).toBe('Cada día');
    expect(pickValue(el, 'Hora')).toBe('09:00');
    expect(pickValue(el, 'Idioma del resumen')).toBe('Español');
    expect(el.querySelector('.pick[aria-label^="Día"]')).toBeNull();
    expect(el.querySelector<HTMLInputElement>('input[name=timezone]')?.value).toBe('Europe/Madrid');
    expect(el.querySelectorAll('.dest')).toHaveLength(2);
    expect(el.querySelector('.mail__subject')?.textContent).toBe(
      'Resumen diario de tienda: 1 nuevo',
    );
    const frame = el.querySelector<HTMLIFrameElement>('.mail__frame');
    expect(frame?.getAttribute('sandbox')).toBe('allow-same-origin');
    expect(frame?.getAttribute('srcdoc')).toContain('<h1>Resumen diario de tienda: 1 nuevo</h1>');
    expect(el.querySelector<HTMLButtonElement>('.send')?.disabled).toBe(false);
  });

  it('pasa a semanal en inglés, recarga la vista previa y guarda con PUT', async () => {
    const { fixture, el } = await render();
    const choose = (label: string, option: string) => {
      el.querySelector<HTMLButtonElement>(`.pick[aria-label^="${label}"]`)?.click();
      fixture.detectChanges();
      Array.from(el.querySelectorAll<HTMLButtonElement>('.menu__item'))
        .find((i) => i.textContent?.trim() === option)
        ?.click();
      fixture.detectChanges();
    };
    choose('Frecuencia', 'Cada semana');
    previewReq('es', 'weekly').flush(preview('Resumen semanal de tienda: sin novedades'));
    choose('Día', 'viernes');
    choose('Hora', '18:00');
    choose('Idioma del resumen', 'English');
    previewReq('en', 'weekly').flush(preview('Weekly digest for tienda: nothing new'));
    await fixture.whenStable();
    fixture.detectChanges();
    expect(el.querySelector('.mail__subject')?.textContent).toBe(
      'Weekly digest for tienda: nothing new',
    );
    expect(el.querySelector<HTMLButtonElement>('.send')?.disabled).toBe(true);
    expect(el.textContent).toContain('Hay cambios sin guardar');

    el.querySelector<HTMLButtonElement>('button[type=submit]')?.click();
    const req = http.expectOne('/api/0/projects/4/digest');
    expect(req.request.method).toBe('PUT');
    expect(req.request.body).toEqual({
      enabled: true,
      frequency: 'weekly',
      weekday: 5,
      hour: 18,
      timezone: 'Europe/Madrid',
      language: 'en',
      send_empty: true,
      destinations: [
        { id: 'correo', type: 'email', to: ['equipo@example.com'] },
        { id: 'hook', type: 'webhook', url: 'https://agente.example.com/resumen', secret: '…a1b2' },
      ],
    });
    req.flush(
      saved({
        frequency: 'weekly',
        weekday: 5,
        hour: 18,
        language: 'en',
        next_send_at: '2026-11-27T17:00:00Z',
      }),
    );
    previewReq('en', 'weekly').flush(preview('Weekly digest for tienda: nothing new'));
    await fixture.whenStable();
    fixture.detectChanges();
    expect(el.querySelector('.toast')?.textContent).toContain('Resumen guardado');
    expect(el.querySelector('.status')?.textContent).toMatch(/vie.*27.*nov.*18:00/);
    expect(el.querySelector<HTMLButtonElement>('.send')?.disabled).toBe(false);
  });

  it('añade un Slack y un webhook nuevo, y enseña el secreto generado una vez', async () => {
    const { fixture, el } = await render(saved({ destinations: [] }));
    expect(el.textContent).toContain('Sin destinos');
    expect(el.querySelector<HTMLButtonElement>('.send')?.disabled).toBe(true);
    const add = (label: string) => {
      el.querySelector<HTMLButtonElement>('.add .btn')?.click();
      fixture.detectChanges();
      Array.from(el.querySelectorAll<HTMLButtonElement>('.add .menu__item'))
        .find((i) => i.textContent?.trim() === label)
        ?.click();
      fixture.detectChanges();
    };
    add('Slack');
    add('Webhook');
    const inputs = el.querySelectorAll<HTMLInputElement>('.dest input');
    inputs[0].value = 'https://hooks.slack.com/services/T0/B0/xyz';
    inputs[0].dispatchEvent(new Event('input'));
    inputs[1].value = 'https://agente.example.com/resumen';
    inputs[1].dispatchEvent(new Event('input'));
    fixture.detectChanges();
    el.querySelector<HTMLButtonElement>('button[type=submit]')?.click();
    const req = http.expectOne('/api/0/projects/4/digest');
    expect(req.request.body.destinations).toMatchObject([
      { type: 'slack', url: 'https://hooks.slack.com/services/T0/B0/xyz' },
      { type: 'webhook', url: 'https://agente.example.com/resumen', secret: '' },
    ]);
    const secret = 'f'.repeat(64);
    const [slack, hook] = req.request.body.destinations;
    req.flush(
      saved({
        destinations: [
          { ...slack, url: 'https://hooks.slack.com/…/xyz' },
          { ...hook, secret },
        ],
      }),
    );
    previewReq('es', 'daily').flush(preview('Resumen diario de tienda: 1 nuevo'));
    await fixture.whenStable();
    fixture.detectChanges();
    expect(el.querySelector('.secret__value')?.textContent).toBe(secret);
    expect(el.querySelectorAll<HTMLInputElement>('.dest input')[2].value).toBe('…ffff');
  });

  it('«Mandar uno ahora» dice cómo ha ido en cada destino', async () => {
    const { fixture, el } = await render();
    el.querySelector<HTMLButtonElement>('.send')?.click();
    fixture.detectChanges();
    expect(el.querySelector('.send')?.textContent).toContain('Mandando…');
    const req = http.expectOne('/api/0/projects/4/digest/send');
    expect(req.request.method).toBe('POST');
    req.flush({
      data: [
        {
          id: 'correo',
          type: 'email',
          ok: false,
          error: 'para avisar por correo hace falta SMTP_URL en el servidor',
        },
        { id: 'hook', type: 'webhook', ok: true },
      ],
    });
    await fixture.whenStable();
    fixture.detectChanges();
    const heads = el.querySelectorAll('.dest__head');
    expect(heads[0].querySelector('.err')?.textContent).toContain('SMTP_URL');
    expect(heads[1].querySelector('.ok')?.textContent).toContain('Enviado');
  });

  it('en inglés no queda texto en español', async () => {
    TestBed.inject(I18n).set('en');
    const { el } = await render(saved({ language: 'en', frequency: 'weekly', weekday: 1 }));
    expect(el.querySelector('.section__title')?.textContent).toContain('Project digest');
    expect(el.querySelector('.status')?.textContent).toMatch(
      /The next one goes out on Mon, Nov 23/,
    );
    expect(pickValue(el, 'Frequency')).toBe('Every week');
    expect(pickValue(el, 'Day')).toBe('Monday');
    expect(el.textContent).toContain('Send one now');
    expect(el.textContent).toContain('Where it goes');
    expect(el.querySelector('.mail__subject')?.textContent).toBe('Daily digest for tienda: 1 new');
    expect(spanishLeft(el)).toEqual([]);
  });
});
