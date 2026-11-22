import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { environment } from '../core/environment';
import { lang } from '../core/i18n';
import { spanishLeft } from '../core/i18n.testing';
import { Session } from '../core/session';
import { Shell } from './shell';

describe('Shell', () => {
  beforeEach(() => {
    lang.set('es');
    localStorage.clear();
    TestBed.configureTestingModule({
      providers: [provideRouter([]), provideHttpClient(), provideHttpClientTesting()],
    });
    TestBed.inject(Session).info.set({
      user: { login: 'ana', name: 'Ana Pérez' },
      methods: ['github'],
    });
  });

  afterEach(() => {
    localStorage.clear();
    lang.set('es');
  });

  function render() {
    const fixture = TestBed.createComponent(Shell);
    fixture.detectChanges();
    return { fixture, el: fixture.nativeElement as HTMLElement };
  }

  const click = (el: HTMLElement, selector: string) =>
    el.querySelector<HTMLButtonElement>(selector)?.click();

  it('el idioma se cambia junto al tema, se recuerda y pone <html lang>', async () => {
    const { fixture, el } = render();
    const buttons = Array.from(el.querySelectorAll('.rail__btn')).map((b) =>
      b.getAttribute('aria-label'),
    );
    expect(buttons).toEqual(['Idioma', 'Tema']);
    expect(el.querySelector('.rail__avatar')?.getAttribute('aria-label')).toBe('Cuenta de ana');

    click(el, '[aria-label="Idioma"]');
    fixture.detectChanges();
    const items = Array.from(el.querySelectorAll<HTMLButtonElement>('[role=menuitemradio]'));
    expect(items.map((i) => [i.textContent?.trim(), i.getAttribute('aria-checked')])).toEqual([
      ['Español', 'true'],
      ['English', 'false'],
    ]);
    expect(items.map((i) => i.getAttribute('lang'))).toEqual(['es', 'en']);

    items[1].click();
    await fixture.whenStable();
    expect(lang()).toBe('en');
    expect(document.documentElement.lang).toBe('en');
    expect(localStorage.getItem('ie-lang')).toBe('en');
    expect(el.querySelector('[role=menu]')).toBeNull();
    expect(
      Array.from(el.querySelectorAll('.rail__btn')).map((b) => b.getAttribute('aria-label')),
    ).toEqual(['Language', 'Theme']);
    expect(el.querySelector('.rail__avatar')?.getAttribute('aria-label')).toBe('Account: ana');
  });

  it('en inglés, el menú del tema y el de la cuenta', async () => {
    lang.set('en');
    const { fixture, el } = render();
    expect(el.querySelector('nav')?.getAttribute('aria-label')).toBe('Main');
    click(el, '[aria-label="Theme"]');
    fixture.detectChanges();
    expect(
      Array.from(el.querySelectorAll('[role=menuitemradio]')).map((i) => i.textContent?.trim()),
    ).toEqual(['Match system', 'Light', 'Dark']);
    expect(el.querySelector('.menu__label')?.textContent).toBe('Theme');
    expect(spanishLeft(el)).toEqual([]);

    click(el, '.menu__veil');
    fixture.detectChanges();
    click(el, '.rail__avatar');
    fixture.detectChanges();
    expect(el.querySelector('[role=menu]')?.getAttribute('aria-label')).toBe('Account');
    expect(el.querySelector('[role=menuitem]')?.textContent?.trim()).toBe('Sign out');
    expect(spanishLeft(el)).toEqual([]);
  });

  describe('el selector de entorno', () => {
    const ENVS = {
      data: [
        { name: 'production', issues: 3, events: 40, last_seen: new Date().toISOString() },
        { name: 'staging', issues: 1, events: 2, last_seen: new Date().toISOString() },
      ],
    };

    afterEach(() => environment.set(''));

    function open(el: HTMLElement, fixture: ReturnType<typeof render>['fixture']) {
      const http = TestBed.inject(HttpTestingController);
      for (const req of http.match('/api/0/environments/')) {
        req.flush(ENVS);
      }
      click(el, '.rail__env');
      fixture.detectChanges();
      // Al abrirlo vuelve a pedir la lista.
      http.expectOne('/api/0/environments/').flush(ENVS);
      fixture.detectChanges();
    }

    it('lista los entornos de los eventos, filtra con el elegido y se recuerda', () => {
      environment.set('');
      const { fixture, el } = render();
      expect(el.querySelector('.rail__env')?.getAttribute('aria-label')).toBe(
        'Entorno: Todos los entornos',
      );
      open(el, fixture);
      const items = Array.from(el.querySelectorAll<HTMLButtonElement>('[role=menuitemradio]'));
      expect(items.map((i) => [i.textContent?.trim(), i.getAttribute('aria-checked')])).toEqual([
        ['Todos los entornos', 'true'],
        ['production', 'false'],
        ['staging', 'false'],
      ]);
      expect(el.querySelector('.rail__env-note')?.textContent).toContain('Se recuerda');

      items[2].click();
      fixture.detectChanges();
      expect(environment()).toBe('staging');
      expect(localStorage.getItem('ie-env')).toBe('staging');
      expect(el.querySelector('[role=menu]')).toBeNull();
      expect(el.querySelector('.rail__env')?.classList).toContain('rail__env--on');
      expect(el.querySelector('.rail__env-name')?.textContent).toBe('staging');
      expect(el.querySelector('.rail__env')?.getAttribute('aria-label')).toBe('Entorno: staging');

      open(el, fixture);
      el.querySelector<HTMLButtonElement>('[role=menuitemradio]')?.click();
      fixture.detectChanges();
      expect(environment()).toBe('');
      expect(localStorage.getItem('ie-env')).toBeNull();
    });

    it('en inglés', () => {
      lang.set('en');
      environment.set('production');
      const { fixture, el } = render();
      expect(el.querySelector('.rail__env')?.getAttribute('aria-label')).toBe(
        'Environment: production',
      );
      open(el, fixture);
      expect(el.querySelector('.menu__label')?.textContent).toBe('Environment');
      expect(
        Array.from(el.querySelectorAll('[role=menuitemradio]')).map((i) => [
          i.textContent?.trim(),
          i.getAttribute('aria-checked'),
        ]),
      ).toEqual([
        ['All environments', 'false'],
        ['production', 'true'],
        ['staging', 'false'],
      ]);
      expect(spanishLeft(el)).toEqual([]);
    });
  });

  it('el raíl lleva a los errores, a las releases, a los monitores y a los ajustes', () => {
    const { el } = render();
    const links = Array.from(el.querySelectorAll<HTMLAnchorElement>('.rail__nav'));
    expect(links.map((a) => [a.getAttribute('href'), a.getAttribute('aria-label')])).toEqual([
      ['/', 'Errores'],
      ['/releases', 'Releases'],
      ['/monitors', 'Monitores'],
      ['/settings', 'Ajustes del proyecto'],
    ]);
  });
});
