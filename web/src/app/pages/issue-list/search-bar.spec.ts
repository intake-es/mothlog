import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { I18n, lang } from '../../core/i18n';
import { spanishLeft } from '../../core/i18n.testing';
import { SearchBar } from './search-bar';

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

describe('SearchBar', () => {
  let http: HttpTestingController;

  beforeEach(() => {
    lang.set('es');
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
  });

  afterEach(() => http.verify());

  async function render(query = 'is:open') {
    const fixture = TestBed.createComponent(SearchBar);
    fixture.componentRef.setInput('query', query);
    fixture.componentRef.setInput('projects', ['api', 'web']);
    fixture.detectChanges();
    const submitted: string[] = [];
    fixture.componentInstance.submitted.subscribe((q) => submitted.push(q));
    const el: HTMLElement = fixture.nativeElement;
    const input = el.querySelector('input') as HTMLInputElement;
    input.dispatchEvent(new Event('focus'));
    http.expectOne('/api/0/tags/').flush({
      data: [
        { key: 'environment', issues: 4 },
        { key: 'browser', issues: 2 },
        { key: 'component', issues: 1 },
      ],
    });
    await fixture.whenStable();
    const type = async (text: string) => {
      input.value = text;
      input.setSelectionRange(text.length, text.length);
      input.dispatchEvent(new Event('input'));
      await fixture.whenStable();
    };
    const key = async (k: string) => {
      input.dispatchEvent(new KeyboardEvent('keydown', { key: k, cancelable: true }));
      await fixture.whenStable();
    };
    const items = () =>
      Array.from(el.querySelectorAll('.suggest__item')).map((b) => [
        b.querySelector('.suggest__code')?.textContent,
        b.querySelector('.suggest__detail')?.textContent ?? '',
      ]);
    return { fixture, el, input, submitted, type, key, items };
  }

  it('autocompleta la clave y luego el valor, y busca al elegirlo', async () => {
    const { fixture, el, input, submitted, type, key, items } = await render();
    await type('is:open bro');
    expect(el.querySelector('.suggest__label')?.textContent).toBe('Claves');
    expect(items()).toEqual([['browser:', 'en 2 errores']]);

    await key('Tab');
    expect(input.value).toBe('is:open browser:');
    await sleep(150);
    const req = http.expectOne((r) => r.url === '/api/0/tags/browser/values/');
    expect(req.request.params.get('prefix')).toBe('');
    req.flush({
      data: [
        { value: 'Safari', issues: 1, count: 3 },
        { value: 'Chrome Headless', issues: 1, count: 1 },
      ],
    });
    await fixture.whenStable();
    expect(el.querySelector('.suggest__label')?.textContent).toBe('Valores de browser');
    expect(items()).toEqual([
      ['Safari', '3 eventos'],
      ['Chrome Headless', '1 evento'],
    ]);

    await key('ArrowDown');
    await key('ArrowDown');
    expect(el.querySelector('.suggest__item--on .suggest__code')?.textContent).toBe(
      'Chrome Headless',
    );
    expect(input.getAttribute('aria-activedescendant')).toBe('suggestion-1');
    await key('Enter');
    expect(submitted).toEqual(['is:open browser:"Chrome Headless"']);
    expect(el.querySelector('.suggest')).toBeNull();
  });

  it('los valores de is:, project: y la ayuda de user:', async () => {
    const { el, type, items } = await render();
    await type('is:re');
    expect(items()).toEqual([
      ['resolved', 'Resueltos'],
      ['resolved_next', 'En la siguiente versión'],
      ['regressed', 'Regresiones'],
    ]);
    await type('!project:w');
    expect(items()).toEqual([['web', '']]);
    await type('user:');
    expect(items()).toEqual([]);
    expect(el.querySelector('.suggest__hint')?.textContent).toBe(
      'Escribe el id, el email o la IP exactos: se guardan con hash.',
    );
  });

  it('los valores de assigned: e is: de asignación, en los dos idiomas', async () => {
    const { fixture, el, submitted, type, items, key } = await render();
    await type('assigned:');
    http.expectOne('/api/0/assignees/').flush({
      data: [
        { name: 'ana', kind: 'user', display_name: 'Ana Pérez', active: true },
        { name: 'dev', kind: 'user', active: true },
        { name: 'agente', kind: 'token', active: true },
      ],
    });
    await fixture.whenStable();
    expect(items()).toEqual([
      ['me', 'Tú'],
      ['ana', 'Ana Pérez'],
      ['dev', 'Persona'],
      ['agente', 'Agente (token)'],
    ]);
    await type('!assigned:a');
    expect(items()).toEqual([
      ['ana', 'Ana Pérez'],
      ['agente', 'Agente (token)'],
    ]);
    await type('is:un');
    expect(items()).toEqual([['unassigned', 'Sin asignar']]);
    await type('is:as');
    expect(items()).toEqual([['assigned', 'Con alguien asignado']]);
    await type('');
    expect(items()).toContainEqual(['assigned:me', 'Los asignados a ti']);
    expect(items()).toContainEqual(['assigned:', 'Quién lo lleva: me, un login o un token']);

    TestBed.inject(I18n).set('en');
    await type('assigned:m');
    expect(items()).toEqual([['me', 'You']]);
    await type('is:un');
    expect(items()).toEqual([['unassigned', 'Unassigned']]);
    await type('!assigned:');
    expect(items()).toContainEqual(['agente', 'Agent (token)']);
    expect(spanishLeft(el)).toEqual([]);
    await key('ArrowDown');
    await key('Enter');
    expect(submitted).toEqual(['!assigned:me']);
    lang.set('es');
  });

  it('sin nada escrito enseña las claves con su explicación y ejemplos', async () => {
    const { el, submitted, type, items } = await render();
    await type('');
    const labels = Array.from(el.querySelectorAll('.suggest__label')).map((l) => l.textContent);
    expect(labels).toEqual(['Claves', 'Ejemplos']);
    expect(items().slice(0, 2)).toEqual([
      ['is:', 'Estado del error'],
      ['release:', 'Release del evento'],
    ]);
    expect(items()).toContainEqual(['os:"Mac OS"', 'En Mac OS: comillas por el espacio']);
    expect(el.querySelector('.suggest__help')?.textContent).toContain('! delante niega');
    Array.from(el.querySelectorAll<HTMLButtonElement>('.suggest__item'))
      .find((b) => b.textContent?.includes('is:regressed'))
      ?.click();
    expect(submitted).toEqual(['is:regressed']);
  });

  it('Intro busca lo escrito y avisa mientras no se ha buscado', async () => {
    const { el, submitted, type, key } = await render();
    expect(el.querySelector('.pending')).toBeNull();
    await type('is:open timeout');
    expect(el.querySelector('.pending')?.textContent).toBe('Intro para buscar');
    await key('Enter');
    expect(submitted).toEqual(['is:open timeout']);
  });

  it('el error de sintaxis, a la vista y en los dos idiomas', async () => {
    const { fixture, el, input } = await render('os:"Mac OS');
    fixture.componentRef.setInput('error', { code: 'unclosed_quote', position: 3 });
    await fixture.whenStable();
    expect(el.querySelector('#search-error')?.textContent?.trim()).toBe(
      'Carácter 4: faltan las comillas de cierre.',
    );
    expect(input.getAttribute('aria-invalid')).toBe('true');
    expect(input.getAttribute('aria-describedby')).toBe('search-error');

    TestBed.inject(I18n).set('en');
    await fixture.whenStable();
    expect(el.querySelector('#search-error')?.textContent?.trim()).toBe(
      'Character 4: the closing quote is missing.',
    );
    fixture.componentRef.setInput('error', {
      code: 'bad_status',
      position: 3,
      key: 'is',
      value: 'abierto',
    });
    await fixture.whenStable();
    expect(el.querySelector('#search-error')?.textContent?.trim()).toBe(
      'Character 4: is:abierto doesn’t exist; use open, resolved, resolved_next, silenced, regressed, assigned or unassigned.',
    );
    expect(spanishLeft(el)).toEqual([]);
  });

  it('en inglés', async () => {
    lang.set('en');
    const { el, input, type, items } = await render();
    expect(input.getAttribute('placeholder')).toBe('Search: text, is:open, browser:Safari…');
    await type('');
    expect(Array.from(el.querySelectorAll('.suggest__label')).map((l) => l.textContent)).toEqual([
      'Keys',
      'Examples',
    ]);
    expect(items()).toContainEqual(['is:', 'Error status']);
    expect(items()).toContainEqual(['browser:', 'in 2 errors']);
    expect(items()).toContainEqual(['is:regressed', 'Back after being resolved']);
    expect(el.querySelector('.suggest__help')?.textContent).toBe(
      '! in front negates · quotes keep spaces together · Enter searches',
    );
    await type('is:sil');
    expect(items()).toEqual([['silenced', 'Silenced']]);
    expect(el.querySelector('.pending')?.textContent).toBe('Press Enter to search');
    expect(el.querySelector('.clear')?.getAttribute('aria-label')).toBe('Clear the search');
    expect(spanishLeft(el)).toEqual([]);
  });
});
