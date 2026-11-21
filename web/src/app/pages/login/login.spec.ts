import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { lang } from '../../core/i18n';
import { spanishLeft } from '../../core/i18n.testing';
import { LoginPage } from './login';

describe('LoginPage', () => {
  let http: HttpTestingController;

  beforeEach(() => {
    lang.set('es');
    TestBed.configureTestingModule({
      providers: [provideRouter([]), provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
  });

  async function render(session: object, inputs: Record<string, string> = {}) {
    const fixture = TestBed.createComponent(LoginPage);
    for (const [k, v] of Object.entries(inputs)) {
      fixture.componentRef.setInput(k, v);
    }
    fixture.detectChanges();
    http.expectOne('/auth/session').flush(session);
    // La sesión llega por una promesa: un turno más y se pinta.
    await new Promise((resolve) => setTimeout(resolve));
    fixture.detectChanges();
    return fixture.nativeElement as HTMLElement;
  }

  it('sin GitHub ni modo de desarrollo dice que el login no está configurado', async () => {
    const el = await render({ user: null, methods: [] });
    expect(el.querySelector('.login__off-title')?.textContent).toBe('El login no está configurado');
    expect(el.textContent).toContain('GITHUB_CLIENT_ID');
    expect(el.querySelector('a.btn')).toBeNull();
  });

  it('con GitHub enlaza al login conservando a dónde volver', async () => {
    const el = await render(
      { user: null, methods: ['github'], org: 'example-org' },
      { next: '/issues/42' },
    );
    const a = el.querySelector<HTMLAnchorElement>('a.btn--primary');
    expect(a?.textContent).toContain('Entrar con GitHub');
    expect(a?.getAttribute('href')).toBe('/auth/github/login?next=%2Fissues%2F42');
    expect(el.textContent).toContain('example-org');
  });

  it('explica por qué no se pudo entrar', async () => {
    const el = await render(
      { user: null, methods: ['github'], org: 'example-org' },
      { error: 'org' },
    );
    expect(el.querySelector('[role=alert]')?.textContent).toContain('no es miembro activo');
  });

  it('en modo de desarrollo ofrece entrar como dev', async () => {
    const el = await render({ user: null, methods: ['dev'] });
    const a = el.querySelector<HTMLAnchorElement>('a.btn--primary');
    expect(a?.getAttribute('href')).toBe('/auth/dev/login');
    expect(el.textContent).toContain('modo de desarrollo');
  });

  describe('en inglés', () => {
    beforeEach(() => lang.set('en'));

    it('dice que el login no está configurado y enumera lo que falta', async () => {
      const el = await render({ user: null, methods: [] });
      expect(el.querySelector('.login__off-title')?.textContent).toBe('Sign-in is not configured');
      expect(el.querySelector('.login__off-text')?.textContent?.replace(/\s+/g, ' ').trim()).toBe(
        'The server is missing GITHUB_CLIENT_ID, GITHUB_CLIENT_SECRET, GITHUB_ORG, and SESSION_SECRET. The API and the MCP still work with tokens.',
      );
      expect(el.querySelectorAll('.login__off-text code').length).toBe(4);
      expect(spanishLeft(el)).toEqual([]);
    });

    it('con GitHub, el botón, la organización y el error', async () => {
      const el = await render(
        { user: null, methods: ['github', 'dev'], org: 'example-org' },
        { error: 'org' },
      );
      expect(el.querySelector('.login__lede')?.textContent?.trim()).toBe(
        'Sign in with your GitHub account in example-org.',
      );
      expect(el.querySelector('.login__lede strong')?.textContent).toBe('example-org');
      expect(el.querySelector('a.btn--primary')?.textContent?.trim()).toBe('Sign in with GitHub');
      expect(el.querySelector('[role=alert]')?.textContent).toContain('not an active member');
      expect(el.textContent).toContain('Sign in as dev');
      expect(el.textContent).toContain('Development mode lets you in without GitHub');
      expect(spanishLeft(el)).toEqual([]);
    });

    it('en español, la lista de variables lleva «y»', async () => {
      lang.set('es');
      const el = await render({ user: null, methods: [] });
      expect(el.querySelector('.login__off-text')?.textContent?.replace(/\s+/g, ' ').trim()).toBe(
        'Faltan GITHUB_CLIENT_ID, GITHUB_CLIENT_SECRET, GITHUB_ORG y SESSION_SECRET en el servidor. La API y el MCP con tokens siguen funcionando.',
      );
    });
  });
});
