import { TestBed } from '@angular/core/testing';
import type { ExceptionValue, Frame } from '../../core/api';
import { parseCrumbs } from './breadcrumbs';
import { codeLines, exceptionViews, groupFrames, isOwn, Stacktrace } from './stacktrace';

const lib = (fn: string): Frame => ({
  filename: `node_modules/zone.js/${fn}.js`,
  function: fn,
  lineno: 1,
  in_app: false,
});
const app: Frame = {
  filename: 'src/app/pages/pagina-de-ejemplo/pagina-de-ejemplo.ts',
  function: 't.failForFixture',
  lineno: 268,
  colno: 25,
  in_app: true,
  pre_context: ['  private failForFixture(data: Respuesta): string {', '    if (!data.cuenta) {'],
  context_line: '    return data.cuenta!.id;',
  post_context: ['  }'],
  minified: { filename: 'chunk-A1B2C3D4.js', lineno: 1, colno: 11129 },
};
const caller: Frame = {
  ...app,
  function: 't.onSubmit',
  lineno: 134,
  context_line: '    this.failForFixture(r);',
  pre_context: [],
  post_context: [],
};

describe('stacktrace', () => {
  it('numera las líneas desde el contexto y marca la del error', () => {
    expect(codeLines(app)).toEqual([
      { n: 266, text: '  private failForFixture(data: Respuesta): string {', error: false },
      { n: 267, text: '    if (!data.cuenta) {', error: false },
      { n: 268, text: '    return data.cuenta!.id;', error: true },
      { n: 269, text: '  }', error: false },
    ]);
    expect(codeLines({ filename: 'x.js' })).toEqual([]);
  });

  it('pone primero el fotograma que falló y pliega las librerías seguidas', () => {
    // Orden del SDK: el más externo primero.
    const groups = groupFrames([lib('a'), lib('b'), caller, lib('c'), app]);
    expect(groups.map((g) => (g.kind === 'app' ? g.frame.fn : `libs:${g.frames.length}`))).toEqual([
      't.failForFixture',
      'libs:1',
      't.onSubmit',
      'libs:2',
    ]);
  });

  it('los .js servidos sin mapa cuentan como librerías aunque el SDK diga in_app', () => {
    expect(isOwn(app)).toBe(true);
    expect(
      isOwn({
        filename: 'https://app.example.com/polyfills-J9K8L7M6.js',
        function: 'f.invokeTask',
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
        value: "Cannot read properties of undefined (reading 'id')",
        mechanism: { type: 'auto.function.angular.error_handler', handled: false },
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
    expect(heads[0].textContent).toContain('pagina-de-ejemplo.ts');
    expect(heads[0].textContent).toContain(':268:25');
    const error = el.querySelector('.code__line--error');
    expect(error?.querySelector('.code__n')?.textContent).toBe('268');
    expect(error?.querySelector('.code__t')?.textContent).toBe('    return data.cuenta!.id;');
    expect(el.querySelector('.frame__min')?.textContent).toContain('chunk-A1B2C3D4.js:1:11129');

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
      '134',
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
