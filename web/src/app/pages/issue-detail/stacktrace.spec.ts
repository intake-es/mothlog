import { TestBed } from '@angular/core/testing';
import type { ExceptionValue, Frame } from '../../core/api';
import { lang } from '../../core/i18n';
import { spanishLeft } from '../../core/i18n.testing';
import { Breadcrumbs, parseCrumbs } from './breadcrumbs';
import { codeLines, exceptionViews, groupFrames, isOwn, Stacktrace } from './stacktrace';

const lib = (fn: string): Frame => ({
  filename: `node_modules/mitt/${fn}.js`,
  function: fn,
  lineno: 1,
  in_app: false,
});
const app: Frame = {
  filename: 'src/carrito.ts',
  function: 'a',
  lineno: 7,
  colno: 25,
  in_app: true,
  pre_context: [
    'function contarUnidades(pedido: Pedido): number {',
    '  // Un pedido recién creado aún no tiene líneas.',
  ],
  context_line: '  return pedido.lineas!.reduce((total, linea) => total + linea.cantidad, 0);',
  post_context: ['}'],
  minified: { filename: 'chunk-Z554BZQ6.js', lineno: 1, colno: 383 },
};
const caller: Frame = {
  ...app,
  function: 'l',
  lineno: 19,
  context_line: "  bus.emit('pedido', { id });",
  pre_context: [],
  post_context: [],
};

describe('stacktrace', () => {
  beforeEach(() => lang.set('es'));

  it('numera las líneas desde el contexto y marca la del error', () => {
    expect(codeLines(app)).toEqual([
      { n: 5, text: 'function contarUnidades(pedido: Pedido): number {', error: false },
      { n: 6, text: '  // Un pedido recién creado aún no tiene líneas.', error: false },
      {
        n: 7,
        text: '  return pedido.lineas!.reduce((total, linea) => total + linea.cantidad, 0);',
        error: true,
      },
      { n: 8, text: '}', error: false },
    ]);
    expect(codeLines({ filename: 'x.js' })).toEqual([]);
  });

  it('pone primero el fotograma que falló y pliega las librerías seguidas', () => {
    // Orden del SDK: el más externo primero.
    const groups = groupFrames([lib('a'), lib('b'), caller, lib('c'), app]);
    expect(groups.map((g) => (g.kind === 'app' ? g.frame.fn : `libs:${g.frames.length}`))).toEqual([
      'a',
      'libs:1',
      'l',
      'libs:2',
    ]);
  });

  it('los .js servidos sin mapa cuentan como librerías aunque el SDK diga in_app', () => {
    expect(isOwn(app)).toBe(true);
    expect(
      isOwn({
        filename: 'https://app.example.com/main-3TO43VUM.js',
        function: 'HTMLButtonElement.r',
        in_app: true,
      }),
    ).toBe(false);
    expect(
      isOwn({ filename: '<anonymous>', function: 'UtilityScript.evaluate', in_app: true }),
    ).toBe(false);
    expect(isOwn({ filename: 'app/pedidos.py', module: 'app.pedidos', in_app: true })).toBe(true);
    expect(isOwn({ filename: 'app/pedidos.py', in_app: false })).toBe(false);
  });

  it('sin fotogramas propios no esconde ninguno', () => {
    const groups = groupFrames([lib('a'), lib('b')]);
    expect(groups.every((g) => g.kind === 'app')).toBe(true);
  });

  it('lee la cadena desde la que se lanzó', () => {
    const views = exceptionViews([
      { type: 'KeyError', value: "'pedido'" },
      { type: 'RuntimeError', value: 'al procesar', mechanism: { type: 'logging', handled: true } },
    ]);
    expect(views.map((v) => v.type)).toEqual(['RuntimeError', 'KeyError']);
    expect(views[0].handled).toBe(true);
    expect(views[1].empty).toBe(true);
  });

  it('abre los propios con su código y deja las librerías en «N fotogramas de librerías»', () => {
    const excs: ExceptionValue[] = [
      {
        type: 'TypeError',
        value: "Cannot read properties of undefined (reading 'reduce')",
        mechanism: { type: 'auto.browser.browserapierrors.addEventListener', handled: false },
        frames: [lib('a'), lib('b'), lib('c'), caller, app],
      },
    ];
    const fixture = TestBed.createComponent(Stacktrace);
    fixture.componentRef.setInput('exceptions', excs);
    fixture.detectChanges();
    const el: HTMLElement = fixture.nativeElement;

    expect(el.querySelector('.exc__type')?.textContent).toBe('TypeError');
    expect(el.querySelector('.exc__mech')?.textContent).toContain('No manejada');
    const heads = el.querySelectorAll('.frame--app .frame__head');
    expect(heads[0].textContent).toContain('src/carrito.ts');
    expect(heads[0].textContent).toContain(':7:25');
    const error = el.querySelector('.code__line--error');
    expect(error?.querySelector('.code__n')?.textContent).toBe('7');
    expect(error?.querySelector('.code__t')?.textContent).toBe(
      '  return pedido.lineas!.reduce((total, linea) => total + linea.cantidad, 0);',
    );
    expect(el.querySelector('.frame__min')?.textContent).toContain('chunk-Z554BZQ6.js:1:383');

    const libs = el.querySelector<HTMLButtonElement>('.libs__toggle');
    expect(libs?.textContent).toContain('3 fotogramas de librerías');
    expect(el.querySelector('.libs__frames')).toBeNull();
    libs?.click();
    fixture.detectChanges();
    expect(el.querySelectorAll('.libs__frames .frame').length).toBe(3);

    // Un propio se puede plegar.
    (heads[0] as HTMLButtonElement).click();
    fixture.detectChanges();
    expect(el.querySelector('.code__line--error')?.querySelector('.code__n')?.textContent).toBe(
      '19',
    );
  });
});

describe('breadcrumbs', () => {
  it('acepta la lista y {values}, y resume data cuando no hay mensaje', () => {
    const crumbs = parseCrumbs([
      { timestamp: 1795090879.029, category: 'navigation', data: { from: '/a', to: '/b' } },
    ]);
    expect(crumbs).toHaveLength(1);
    expect(crumbs[0].category).toBe('navigation');
    expect(crumbs[0].message).toBe('from: /a · to: /b');
    expect(crumbs[0].time).toMatch(/\d\d:\d\d:\d\d/);
    expect(parseCrumbs({ values: [{ message: 'hola', level: 'error' }] })[0]).toEqual({
      time: '',
      category: 'default',
      message: 'hola',
      level: 'error',
    });
    expect(parseCrumbs({ values: [] })).toEqual([]);
    expect(parseCrumbs(undefined)).toEqual([]);
  });
});

describe('stacktrace y breadcrumbs en inglés', () => {
  beforeEach(() => lang.set('en'));
  afterEach(() => lang.set('es'));

  it('marcas, librerías, minificado y la región del código', () => {
    const fixture = TestBed.createComponent(Stacktrace);
    fixture.componentRef.setInput('exceptions', [
      { type: 'Error', value: 'outer', mechanism: { handled: true }, frames: [] },
      {
        type: 'TypeError',
        value: 'x',
        mechanism: { type: 'generic', handled: false },
        frames: [lib('a'), lib('b'), lib('c'), { function: 'f', in_app: true }, app],
      },
    ] satisfies ExceptionValue[]);
    fixture.detectChanges();
    const el: HTMLElement = fixture.nativeElement;
    expect(el.querySelector('.exc__mech')?.textContent).toContain('Unhandled');
    expect(el.querySelectorAll('.exc__mech')[1]?.textContent).toContain('Handled');
    expect(el.querySelector('.exc__caused')?.textContent).toBe('Caused by');
    expect(el.querySelector('.exc__empty')?.textContent).toBe('No frames.');
    expect(el.querySelector('.libs__toggle')?.textContent).toContain('3 library frames');
    expect(el.querySelector('.frame__in')?.textContent).toBe('in');
    expect(el.querySelector('.frame__min')?.textContent).toBe('Minified: chunk-Z554BZQ6.js:1:383');
    expect(el.querySelector('.code')?.getAttribute('aria-label')).toBe('Code of src/carrito.ts');
    expect(el.textContent).toContain('(unknown file)');
    expect(spanishLeft(el)).toEqual([]);
  });

  it('sin breadcrumbs lo dice en inglés', () => {
    const fixture = TestBed.createComponent(Breadcrumbs);
    fixture.componentRef.setInput('raw', []);
    fixture.detectChanges();
    expect(fixture.nativeElement.textContent.trim()).toBe('This event has no breadcrumbs.');
  });
});
