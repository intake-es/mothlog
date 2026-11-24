import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import type { Integrations } from '../../core/api';
import { lang } from '../../core/i18n';
import { spanishLeft } from '../../core/i18n.testing';
import { IntegrationsSection } from './integrations';

const DATA: Integrations = {
  linear: {
    webhook_url: 'https://errors.example.com/webhooks/linear',
    secret_set: true,
    token_set: true,
    last_delivery: new Date(Date.now() - 3 * 60_000).toISOString(),
  },
  github: {
    webhook_url: 'https://errors.example.com/webhooks/github',
    secret_set: false,
    token_set: true,
    last_delivery: null,
  },
};

describe('IntegrationsSection', () => {
  let http: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
  });

  afterEach(() => {
    http.verify();
    lang.set('es');
  });

  async function render(data: Integrations = DATA) {
    const fixture = TestBed.createComponent(IntegrationsSection);
    fixture.detectChanges();
    http.expectOne('/api/0/integrations/').flush(data);
    await fixture.whenStable();
    fixture.detectChanges();
    return { fixture, el: fixture.nativeElement as HTMLElement };
  }

  it('da las URL de los webhooks y dice qué falta en el servidor', async () => {
    lang.set('es');
    const { el } = await render();
    const cards = el.querySelectorAll('.card');
    expect(cards).toHaveLength(2);
    expect(cards[0].querySelector('.integ__code')?.textContent).toBe(
      'https://errors.example.com/webhooks/linear',
    );
    expect(cards[0].querySelector('.integ__head .sp-badge')?.textContent).toBe('Conectado');
    expect(cards[0].textContent).toContain('Se comprueba con LINEAR_WEBHOOK_SECRET');
    expect(cards[0].textContent).toContain('Con LINEAR_API_KEY');
    expect(cards[0].querySelector('time')?.textContent).toContain('hace');
    expect(cards[1].querySelector('.integ__head .sp-badge')?.textContent).toBe('A medias');
    expect(cards[1].textContent).toContain('Falta GITHUB_WEBHOOK_SECRET: se rechazan sus webhooks');
    expect(cards[1].textContent).toContain('Ninguna todavía');
    expect(cards[1].textContent).toContain('Fixes');
  });

  it('sin nada configurado lo dice', async () => {
    lang.set('es');
    const off = { ...DATA.github, token_set: false };
    const { el } = await render({ linear: off, github: off });
    for (const badge of Array.from(el.querySelectorAll('.integ__head .sp-badge'))) {
      expect(badge.textContent).toBe('Sin configurar');
    }
  });

  it('en inglés', async () => {
    lang.set('en');
    const { el } = await render();
    expect(el.querySelector('.section__title')?.textContent).toBe('Linear and GitHub');
    expect(el.textContent).toContain('Verified with LINEAR_WEBHOOK_SECRET');
    expect(el.textContent).toContain('GITHUB_WEBHOOK_SECRET is missing: its webhooks are rejected');
    expect(el.textContent).toContain('None yet');
    expect(el.querySelectorAll('.btn')[0].textContent).toContain('Copy URL');
    expect(spanishLeft(el)).toEqual([]);
  });
});
