import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { LoginPage } from './login';

describe('LoginPage', () => {
  let http: HttpTestingController;

  beforeEach(() => {
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
});
