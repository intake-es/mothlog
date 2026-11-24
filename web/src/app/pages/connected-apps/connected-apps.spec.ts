import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { lang } from '../../core/i18n';
import { spanishLeft } from '../../core/i18n.testing';
import type { OAuthGrant } from '../../core/oauth';
import { ConnectedAppsPage } from './connected-apps';

const GRANTS: OAuthGrant[] = [
  {
    id: 7,
    client_id: 'iec_1',
    client_name: 'Editor de prueba',
    redirect_uri: 'http://127.0.0.1:43123/callback',
    scopes: ['read', 'write'],
    created_at: new Date(Date.now() - 3 * 3_600_000).toISOString(),
    last_used_at: new Date(Date.now() - 5 * 60_000).toISOString(),
    last_used_ip: '198.51.100.7',
  },
  {
    id: 8,
    client_id: 'https://app.example.com/client.json',
    client_name: 'otra app',
    redirect_uri: 'https://app.example.com/cb',
    scopes: ['read'],
    created_at: new Date(Date.now() - 86_400_000).toISOString(),
    last_used_at: null,
    last_used_ip: null,
  },
];

describe('ConnectedAppsPage', () => {
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

  async function render(data: OAuthGrant[] = GRANTS) {
    const fixture = TestBed.createComponent(ConnectedAppsPage);
    fixture.detectChanges();
    http.expectOne('/api/0/oauth/grants/').flush({ data });
    await fixture.whenStable();
    fixture.detectChanges();
    return { fixture, el: fixture.nativeElement as HTMLElement };
  }

  it('enseña cada aplicación con sus permisos y su último uso', async () => {
    const { el } = await render();
    const apps = el.querySelectorAll('.app');
    expect(apps).toHaveLength(2);
    expect(apps[0].querySelector('.app__name')?.textContent).toBe('Editor de prueba');
    expect(apps[0].querySelector('app-mark')?.textContent?.trim()).toBe('E');
    expect(
      Array.from(apps[0].querySelectorAll('.sp-badge')).map((b) => b.textContent?.trim()),
    ).toEqual(['Leer', 'Escribir']);
    expect(apps[0].textContent).toContain('desde 198.51.100.7');
    expect(apps[0].textContent).toContain('Vuelve a 127.0.0.1:43123');
    expect(apps[1].querySelector('app-mark')?.textContent?.trim()).toBe('O');
    expect(apps[1].textContent).toContain('sin usar todavía');
    expect(el.querySelector('img')).toBeNull();
  });

  it('revoca con el modal de la página, no con confirm()', async () => {
    const confirmSpy = vi.spyOn(window, 'confirm');
    const { fixture, el } = await render();
    el.querySelector<HTMLButtonElement>('.app__revoke')?.click();
    fixture.detectChanges();
    const dialog = el.querySelector('[role=alertdialog]');
    expect(dialog?.textContent).toContain('¿Revocar el acceso de Editor de prueba?');
    expect(document.activeElement?.textContent?.trim()).toBe('Cancelar');
    const confirm = dialog?.querySelectorAll<HTMLButtonElement>('button')[1];
    confirm?.click();
    const req = http.expectOne('/api/0/oauth/grants/7');
    expect(req.request.method).toBe('DELETE');
    req.flush(null, { status: 204, statusText: 'No Content' });
    fixture.detectChanges();
    expect(el.querySelector('[role=alertdialog]')).toBeNull();
    expect(el.querySelectorAll('.app')).toHaveLength(1);
    expect(confirmSpy).not.toHaveBeenCalled();
  });

  it('cancelar o Escape cierran el modal sin revocar', async () => {
    const { fixture, el } = await render();
    el.querySelector<HTMLButtonElement>('.app__revoke')?.click();
    fixture.detectChanges();
    el.querySelector('app-confirm-dialog')?.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }),
    );
    fixture.detectChanges();
    expect(el.querySelector('[role=alertdialog]')).toBeNull();
    expect(el.querySelectorAll('.app')).toHaveLength(2);
  });

  it('si no se puede revocar, lo dice en el modal', async () => {
    const { fixture, el } = await render();
    el.querySelector<HTMLButtonElement>('.app__revoke')?.click();
    fixture.detectChanges();
    el.querySelectorAll<HTMLButtonElement>('[role=alertdialog] button')[1]?.click();
    http
      .expectOne('/api/0/oauth/grants/7')
      .flush({ error: 'x' }, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(el.querySelector('[role=alertdialog] [role=alert]')?.textContent).toContain(
      'No se ha podido revocar',
    );
    expect(el.querySelectorAll('.app')).toHaveLength(2);
  });

  it('sin aplicaciones', async () => {
    const { el } = await render([]);
    expect(el.textContent).toContain('Ninguna aplicación conectada');
  });

  it('en inglés no queda texto en español', async () => {
    lang.set('en');
    const { fixture, el } = await render();
    el.querySelector<HTMLButtonElement>('.app__revoke')?.click();
    fixture.detectChanges();
    expect(spanishLeft(el)).toEqual([]);
    expect(el.textContent).toContain('Revoke access for Editor de prueba?');
  });
});
