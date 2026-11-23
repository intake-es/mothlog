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
    expect(el.querySelector('.login__off-oidc')?.textContent?.replace(/\s+/g, ' ').trim()).toBe(
      'Para entrar con un proveedor OIDC, hacen falta OIDC_ISSUER, OIDC_CLIENT_ID y OIDC_ALLOWED_DOMAINS u OIDC_ALLOWED_GROUPS, además de SESSION_SECRET.',
    );
    expect([...el.querySelectorAll('.login__off-oidc code')].map((c) => c.textContent)).toEqual([
      'OIDC_ISSUER',
      'OIDC_CLIENT_ID',
      'OIDC_ALLOWED_DOMAINS',
      'OIDC_ALLOWED_GROUPS',
    ]);
  });

  it('OIDC sin configurar tiene su propio aviso', async () => {
    const el = await render(
      { user: null, methods: ['github'], org: 'example-org' },
      { error: 'oidc_config' },
    );
    expect(el.querySelector('[role=alert]')?.textContent?.trim()).toBe(
      'El login con OIDC no está configurado en este servidor.',
    );
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

  it('en modo de desarrollo pide la clave del log para entrar como dev', async () => {
    const el = await render({ user: null, methods: ['dev'] }, { next: '/issues/7' });
    const form = el.querySelector<HTMLFormElement>('form.login__dev');
    expect(form?.getAttribute('action')).toBe('/auth/dev/login');
    expect(form?.getAttribute('method')).toBe('get');
    const key = form?.querySelector<HTMLInputElement>('input[name=key]');
    expect(key?.type).toBe('password');
    expect(key?.required).toBe(true);
    expect(key?.getAttribute('autocomplete')).toBe('off');
    expect(form?.querySelector<HTMLInputElement>('input[name=next]')?.value).toBe('/issues/7');
    const submit = form?.querySelector<HTMLButtonElement>('button[type=submit]');
    expect(submit?.classList).toContain('btn--primary');
    expect(submit?.textContent?.trim()).toBe('Entrar como dev');
    expect(el.querySelector('a[href^="/auth/dev/login"]')).toBeNull();
    expect(el.querySelector('.login__label')?.textContent).toBe('Clave de desarrollo');
    expect(el.querySelector('.login__note')?.textContent).toContain('escribe en su log un enlace');
    expect(el.textContent).toContain('modo de desarrollo');
  });

  it('sin next, el formulario de dev no lo manda', async () => {
    const el = await render({ user: null, methods: ['dev'] });
    expect(el.querySelector('form.login__dev input[name=next]')).toBeNull();
  });

  it('si la clave no es la del log, lo dice', async () => {
    const el = await render({ user: null, methods: ['dev'] }, { error: 'dev_key' });
    expect(el.querySelector('[role=alert]')?.textContent).toContain(
      'no es la clave de este arranque',
    );
  });

  it('solo con OIDC, un botón con el nombre del proveedor', async () => {
    const el = await render(
      { user: null, methods: ['oidc'], oidc_name: 'Acme SSO' },
      { next: '/issues/42' },
    );
    const buttons = el.querySelectorAll<HTMLAnchorElement>('a.btn');
    expect(buttons.length).toBe(1);
    expect(buttons[0].classList).toContain('btn--primary');
    expect(buttons[0].textContent?.trim()).toBe('Entrar con Acme SSO');
    expect(buttons[0].getAttribute('href')).toBe('/auth/oidc/login?next=%2Fissues%2F42');
    expect(el.querySelector('.login__lede')?.textContent?.trim()).toBe(
      'Entra con tu cuenta de Acme SSO.',
    );
  });

  it('con GitHub y OIDC, un botón por cada uno', async () => {
    const el = await render({
      user: null,
      methods: ['github', 'oidc'],
      org: 'example-org',
      oidc_name: 'Acme SSO',
    });
    const buttons = [...el.querySelectorAll<HTMLAnchorElement>('a.btn')];
    expect(buttons.map((a) => a.getAttribute('href'))).toEqual([
      '/auth/github/login',
      '/auth/oidc/login',
    ]);
    expect(buttons.map((a) => a.classList.contains('btn--primary'))).toEqual([true, false]);
    expect(buttons[1].textContent?.trim()).toBe('Entrar con Acme SSO');
    expect(el.querySelector('.login__lede')?.textContent?.trim()).toBe(
      'Elige con qué cuenta entrar.',
    );
  });

  for (const [error, text] of [
    ['domain', 'no es de ningún dominio con acceso'],
    ['group', 'no está en ningún grupo con acceso'],
    ['oidc', 'Acme SSO no ha respondido'],
    ['oidc_denied', 'Has cancelado el acceso en Acme SSO.'],
    ['save', 'No se ha podido guardar quién eres'],
  ]) {
    it(`explica el rechazo ${error} de OIDC con el nombre del proveedor`, async () => {
      const el = await render({ user: null, methods: ['oidc'], oidc_name: 'Acme SSO' }, { error });
      expect(el.querySelector('[role=alert]')?.textContent).toContain(text);
    });
  }

  describe('en inglés', () => {
    beforeEach(() => lang.set('en'));

    it('dice que el login no está configurado y enumera lo que falta', async () => {
      const el = await render({ user: null, methods: [] });
      expect(el.querySelector('.login__off-title')?.textContent).toBe('Sign-in is not configured');
      expect(el.querySelector('.login__off-text')?.textContent?.replace(/\s+/g, ' ').trim()).toBe(
        'The server is missing GITHUB_CLIENT_ID, GITHUB_CLIENT_SECRET, GITHUB_ORG, and SESSION_SECRET. The API and the MCP still work with tokens.',
      );
      expect(el.querySelectorAll('.login__off-text:not(.login__off-oidc) code').length).toBe(4);
      expect(el.querySelector('.login__off-oidc')?.textContent?.replace(/\s+/g, ' ').trim()).toBe(
        'To sign in with an OIDC provider, the server needs OIDC_ISSUER, OIDC_CLIENT_ID, and OIDC_ALLOWED_DOMAINS or OIDC_ALLOWED_GROUPS, plus SESSION_SECRET.',
      );
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
      expect(el.querySelector('.login__label')?.textContent).toBe('Development key');
      expect(el.querySelector<HTMLInputElement>('input[name=key]')?.placeholder).toBe(
        'From the link in the log',
      );
      expect(el.querySelector('form.login__dev button')?.classList).not.toContain('btn--primary');
      expect(el.querySelector('.login__note')?.textContent).toContain(
        'writes a sign-in link to its log',
      );
      expect(spanishLeft(el)).toEqual([]);
    });

    it('la clave de desarrollo que no vale', async () => {
      const el = await render({ user: null, methods: ['dev'] }, { error: 'dev_key' });
      expect(el.querySelector('[role=alert]')?.textContent).toContain('isn’t the key for this run');
      expect(spanishLeft(el)).toEqual([]);
    });

    it('con GitHub y OIDC, los dos botones y el rechazo por dominio', async () => {
      const el = await render(
        { user: null, methods: ['github', 'oidc'], org: 'example-org', oidc_name: 'Acme SSO' },
        { error: 'domain' },
      );
      expect([...el.querySelectorAll('a.btn')].map((a) => a.textContent?.trim())).toEqual([
        'Sign in with GitHub',
        'Sign in with Acme SSO',
      ]);
      expect(el.querySelector('.login__lede')?.textContent?.trim()).toBe(
        'Choose an account to sign in with.',
      );
      expect(el.querySelector('[role=alert]')?.textContent).toContain(
        'isn’t on a domain with access',
      );
      expect(spanishLeft(el)).toEqual([]);
    });

    it('solo con OIDC, el botón y los rechazos', async () => {
      const el = await render(
        { user: null, methods: ['oidc'], oidc_name: 'Acme SSO' },
        { error: 'group' },
      );
      expect(el.querySelector('a.btn--primary')?.textContent?.trim()).toBe('Sign in with Acme SSO');
      expect(el.querySelector('.login__lede')?.textContent?.trim()).toBe(
        'Sign in with your Acme SSO account.',
      );
      expect(el.querySelector('[role=alert]')?.textContent).toContain('isn’t in any group');
      expect(spanishLeft(el)).toEqual([]);
    });

    it('el rechazo en el proveedor lleva su nombre', async () => {
      const el = await render(
        { user: null, methods: ['oidc'], oidc_name: 'Acme SSO' },
        { error: 'oidc_denied' },
      );
      expect(el.querySelector('[role=alert]')?.textContent?.trim()).toBe(
        'You cancelled access on Acme SSO.',
      );
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
