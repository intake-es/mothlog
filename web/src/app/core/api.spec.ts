import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { Api, apiError } from './api';

describe('Api', () => {
  let api: Api;
  let http: HttpTestingController;

  beforeEach(() => {
    TestBed.configureTestingModule({
      providers: [provideHttpClient(), provideHttpClientTesting()],
    });
    api = TestBed.inject(Api);
    http = TestBed.inject(HttpTestingController);
  });

  afterEach(() => http.verify());

  it('pide la lista con la serie diaria y solo los filtros con valor', () => {
    let total = 0;
    api
      .issues({ project: 'backend', status: 'all', query: '', sort: 'count', limit: 50 })
      .subscribe((page) => {
        total = page.data.length;
      });
    const req = http.expectOne((r) => r.url === '/api/0/issues/');
    expect(req.request.method).toBe('GET');
    expect(req.request.params.get('daily')).toBe('1');
    expect(req.request.params.get('project')).toBe('backend');
    expect(req.request.params.get('status')).toBe('all');
    expect(req.request.params.get('sort')).toBe('count');
    expect(req.request.params.get('limit')).toBe('50');
    expect(req.request.params.has('query')).toBe(false);
    expect(req.request.params.has('cursor')).toBe(false);
    req.flush({ data: [{ id: 1 }, { id: 2 }] });
    expect(total).toBe(2);
  });

  it('cambia el estado con POST y el cuerpo que espera la API', () => {
    api.setStatus(7, 'resolved_next').subscribe();
    const req = http.expectOne('/api/0/issues/7/status');
    expect(req.request.method).toBe('POST');
    expect(req.request.body).toEqual({ status: 'resolved_next' });
    req.flush({ id: 7, status: 'resolved_next' });
  });

  it('lee el stacktrace.md como texto', () => {
    let md = '';
    api.stacktrace(3).subscribe((text) => {
      md = text;
    });
    const req = http.expectOne('/api/0/issues/3/stacktrace.md');
    expect(req.request.responseType).toBe('text');
    req.flush('# TypeError');
    expect(md).toBe('# TypeError');
  });

  it('pagina los eventos con cursor', () => {
    api.events(3, 10, 'abc').subscribe();
    const req = http.expectOne((r) => r.url === '/api/0/issues/3/events/');
    expect(req.request.params.get('limit')).toBe('10');
    expect(req.request.params.get('cursor')).toBe('abc');
    req.flush({ data: [] });
  });

  it('saca el mensaje de los errores de la API', () => {
    expect(apiError({ status: 409, error: { error: 'el último evento no tiene release' } })).toBe(
      'el último evento no tiene release',
    );
    expect(apiError({ status: 404, error: '{"error":"no encontrado"}' })).toBe('no encontrado');
    expect(apiError({ status: 0 })).toBe('No hay conexión con el servidor.');
    expect(apiError({ status: 500, error: 'texto' })).toBe('Algo ha fallado en el servidor.');
  });
});
