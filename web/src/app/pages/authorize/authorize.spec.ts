import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { lang } from '../../core/i18n';
import { spanishLeft } from '../../core/i18n.testing';
import { Browser, type OAuthRequest } from '../../core/oauth';
import { AuthorizePage } from './authorize';

const REQUEST: OAuthRequest = {
  client_name: 'Editor de prueba',
  client_id: 'iec_123',
  client_url: false,
  redirect_uri: 'http://127.0.0.1:43123/callback',
  scopes: ['read', 'write'],
  expires_at: new Date(Date.now() + 600_000).toISOString(),
};

describe('AuthorizePage', () => {
  let http: HttpTestingController;
  let went: string[];

  beforeEach(() => {
    lang.set('es');
    went = [];
    TestBed.configureTestingModule({
      providers: [
        provideHttpClient(),
        provideHttpClientTesting(),
        provideRouter([]),
        { provide: Browser, useValue: { go: (url: string) => went.push(url) } },
      ],
    });
    http = TestBed.inject(HttpTestingController);
  });

  afterEach(() => {
    http.verify();
    lang.set('es');
  });

  async function render(
    inputs: { request?: string; error?: string },
    req: OAuthRequest | 404 = REQUEST,
  ) {
    const fixture = TestBed.createComponent(AuthorizePage);
    for (const [k, v] of Object.entries(inputs)) {
      fixture.componentRef.setInput(k, v);
    }
    fixture.detectChanges();
    http
      .expectOne('/auth/session')
      .flush({ user: { login: 'ana', name: 'Ana' }, methods: ['dev'] });
    if (inputs.request && !inputs.error) {
      const r = http.expectOne(`/api/0/oauth/requests/${inputs.request}`);
      if (req === 404) {
        r.flush({ error: 'no' }, { status: 404, statusText: 'Not Found' });
      } else {
        r.flush(req);
      }
    }
    await fixture.whenStable();
    fixture.detectChanges();
    return { fixture, el: fixture.nativeElement as HTMLElement };
  }

  function boxes(el: HTMLElement) {
    return Array.from(el.querySelectorAll<HTMLInputElement>('input[type=checkbox]'));
  }

  it('enseña la aplicación, adónde vuelve y los permisos, con escribir sin marcar', async () => {
    const { el } = await render({ request: 'abc' });
    expect(el.querySelector('h1')?.textContent).toContain('Conectar Editor de prueba');
    expect(el.querySelector('app-mark')?.textContent?.trim()).toBe('E');
    expect(el.querySelector('img')).toBeNull();
    expect(el.textContent).toContain('http://127.0.0.1:43123/callback');
    expect(el.textContent).toContain('ana');
    expect(el.textContent).toContain('«ana vía Editor de prueba»');
    const [read, write] = boxes(el);
    expect(read.name).toBe('scope-read');
    expect(read.checked).toBe(true);
    expect(write.name).toBe('scope-write');
    expect(write.checked).toBe(false);
  });

  it('concede solo lo marcado y vuelve a la aplicación', async () => {
    const { fixture, el } = await render({ request: 'abc' });
    const [read, write] = boxes(el);
    write.click();
    read.click();
    fixture.detectChanges();
    const allow = el.querySelector<HTMLButtonElement>('.btn--primary');
    allow?.click();
    const req = http.expectOne('/api/0/oauth/requests/abc/approve');
    expect(req.request.method).toBe('POST');
    expect(req.request.body).toEqual({ scopes: ['write'] });
    req.flush({ redirect_to: 'http://127.0.0.1:43123/callback?code=x&state=y' });
    fixture.detectChanges();
    expect(went).toEqual(['http://127.0.0.1:43123/callback?code=x&state=y']);
    expect(el.textContent).toContain('Volviendo a 127.0.0.1:43123');
  });

  it('sin ningún permiso marcado no deja dar acceso', async () => {
    const { fixture, el } = await render({ request: 'abc' });
    boxes(el)[0].click();
    fixture.detectChanges();
    expect(el.querySelector<HTMLButtonElement>('.btn--primary')?.disabled).toBe(true);
    expect(el.textContent).toContain('Marca al menos un permiso.');
  });

  it('cancelar deniega y vuelve con el error', async () => {
    const { el } = await render({ request: 'abc' });
    el.querySelector<HTMLButtonElement>('.consent__actions .btn:not(.btn--primary)')?.click();
    http
      .expectOne('/api/0/oauth/requests/abc/deny')
      .flush({ redirect_to: 'http://127.0.0.1:43123/callback?error=access_denied' });
    expect(went).toEqual(['http://127.0.0.1:43123/callback?error=access_denied']);
  });

  it('solo enseña los permisos que pide', async () => {
    const { el } = await render({ request: 'abc' }, { ...REQUEST, scopes: ['read'] });
    expect(boxes(el).map((b) => b.name)).toEqual(['scope-read']);
  });

  it('con una URL como client_id, enseña dónde está su ficha', async () => {
    const { el } = await render(
      { request: 'abc' },
      { ...REQUEST, client_id: 'https://app.example.com/oauth/client.json', client_url: true },
    );
    expect(el.textContent).toContain('https://app.example.com/oauth/client.json');
  });

  it('una petición caducada o ya respondida', async () => {
    const { el } = await render({ request: 'abc' }, 404);
    expect(el.textContent).toContain('Esta petición ya no vale');
    expect(boxes(el)).toHaveLength(0);
  });

  it('explica por qué no se puede dar acceso', async () => {
    const { el } = await render({ error: 'redirect' });
    expect(el.querySelector('[role=alert]')?.textContent).toContain(
      'no es ninguna de las que registró la aplicación',
    );
  });

  it('en inglés no queda texto en español', async () => {
    lang.set('en');
    const { el } = await render({ request: 'abc' });
    expect(spanishLeft(el)).toEqual([]);
    expect(el.textContent).toContain('Connect Editor de prueba');
  });
});
