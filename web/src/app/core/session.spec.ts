import { HttpClient, provideHttpClient, withInterceptors } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { Component } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { provideRouter, Router, type UrlTree } from '@angular/router';
import { authGuard, loginGuard, Session, unauthorizedInterceptor } from './session';
import { applyTheme } from './theme';

@Component({ template: '' })
class Blank {}

describe('Session', () => {
  let http: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [
        provideRouter([{ path: 'login', component: Blank }]),
        provideHttpClient(withInterceptors([unauthorizedInterceptor])),
        provideHttpClientTesting(),
      ],
    });
    http = TestBed.inject(HttpTestingController);
  });

  afterEach(() => http.verify());

  it('sin sesión, el guard manda al login con a dónde volver', async () => {
    const result = TestBed.runInInjectionContext(() =>
      authGuard({} as never, { url: '/issues/42' } as never),
    ) as Promise<UrlTree>;
    http.expectOne('/auth/session').flush({ user: null, methods: ['github'] });
    const tree = await result;
    expect(TestBed.inject(Router).serializeUrl(tree)).toBe('/login?next=%2Fissues%2F42');
  });

  it('con sesión, deja pasar', async () => {
    const result = TestBed.runInInjectionContext(() =>
      authGuard({} as never, { url: '/' } as never),
    );
    http
      .expectOne('/auth/session')
      .flush({ user: { login: 'dev', name: 'dev' }, methods: ['dev'] });
    expect(await result).toBe(true);
  });

  it('con sesión, /login lleva a la lista', async () => {
    const result = TestBed.runInInjectionContext(() =>
      loginGuard({} as never, { url: '/login' } as never),
    ) as Promise<UrlTree>;
    http
      .expectOne('/auth/session')
      .flush({ user: { login: 'ana', name: 'Ana' }, methods: ['github'] });
    expect(TestBed.inject(Router).serializeUrl(await result)).toBe('/');
  });

  it('sin sesión, /login se queda', async () => {
    const result = TestBed.runInInjectionContext(() =>
      loginGuard({} as never, { url: '/login' } as never),
    );
    http.expectOne('/auth/session').flush({ user: null, methods: ['github'] });
    expect(await result).toBe(true);
  });

  it('un 401 de la API vuelve al login', async () => {
    const session = TestBed.inject(Session);
    const expired = vi.spyOn(session, 'expired').mockImplementation(() => undefined);
    TestBed.inject(HttpClient)
      .get('/api/0/issues/')
      .subscribe({ error: () => undefined });
    http
      .expectOne('/api/0/issues/')
      .flush({ error: 'x' }, { status: 401, statusText: 'Unauthorized' });
    expect(expired).toHaveBeenCalledOnce();
  });

  it('cerrar sesión hace POST a /auth/logout', async () => {
    const session = TestBed.inject(Session);
    const done = session.logout();
    http
      .expectOne({ method: 'POST', url: '/auth/logout' })
      .flush(null, { status: 204, statusText: 'No Content' });
    await new Promise((resolve) => setTimeout(resolve));
    http.expectOne('/auth/session').flush({ user: null, methods: ['dev'] });
    await done;
    expect(session.info()?.user).toBeNull();
  });
});

describe('applyTheme', () => {
  it('fija claro u oscuro a mano y sin clase sigue al sistema', () => {
    const root = document.createElement('html');
    applyTheme('dark', root);
    expect(root.className).toBe('dark');
    applyTheme('light', root);
    expect(root.className).toBe('light');
    applyTheme('system', root);
    expect(root.className).toBe('');
  });
});
