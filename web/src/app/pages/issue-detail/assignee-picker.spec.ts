import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import type { Assignee, Issue } from '../../core/api';
import { lang } from '../../core/i18n';
import { spanishLeft } from '../../core/i18n.testing';
import { Session } from '../../core/session';
import { AssigneePicker } from './assignee-picker';

const options: Assignee[] = [
  {
    name: 'ana',
    kind: 'user',
    display_name: 'Ana Pérez',
    avatar_url: 'https://avatars.example.com/ana',
    active: true,
  },
  { name: 'dev', kind: 'user', active: true },
  { name: 'agente', kind: 'token', active: true },
];

describe('AssigneePicker', () => {
  let http: HttpTestingController;

  beforeEach(() => {
    lang.set('es');
    TestBed.configureTestingModule({
      providers: [provideRouter([]), provideHttpClient(), provideHttpClientTesting()],
    });
    http = TestBed.inject(HttpTestingController);
    TestBed.inject(Session).info.set({ user: { login: 'dev', name: 'Dev' }, methods: ['dev'] });
  });

  afterEach(() => {
    http.verify();
    lang.set('es');
  });

  async function render(assignee: Assignee | null = null) {
    const fixture = TestBed.createComponent(AssigneePicker);
    fixture.componentRef.setInput('issueId', 7);
    fixture.componentRef.setInput('assignee', assignee);
    const changed: Issue[] = [];
    const notices: string[] = [];
    fixture.componentInstance.changed.subscribe((i) => changed.push(i));
    fixture.componentInstance.notice.subscribe((n) => notices.push(n));
    fixture.detectChanges();
    await fixture.whenStable();
    return { fixture, el: fixture.nativeElement as HTMLElement, changed, notices };
  }

  async function open(fixture: Awaited<ReturnType<typeof render>>['fixture']) {
    const el: HTMLElement = fixture.nativeElement;
    el.querySelector<HTMLButtonElement>('.trigger')?.click();
    fixture.detectChanges();
    http.expectOne('/api/0/assignees/').flush({ data: options });
    await fixture.whenStable();
  }

  const items = (el: HTMLElement) =>
    Array.from(el.querySelectorAll<HTMLButtonElement>('.menu__item'));
  const text = (n: Element | null | undefined) =>
    (n?.textContent ?? '').replace(/\s+/g, ' ').trim();

  it('sin asignar, se asigna a quien mira con «Asignármelo»', async () => {
    const { fixture, el, changed, notices } = await render();
    expect(text(el.querySelector('.trigger'))).toBe('Sin asignar');
    expect(el.querySelector('.trigger')?.getAttribute('aria-label')).toBe('Asignado: Sin asignar');
    await open(fixture);
    expect(items(el).map(text)).toEqual(['Asignármelo', 'Ana Pérez ana', 'D dev', 'agente']);
    expect(Array.from(el.querySelectorAll('.menu__label')).map(text)).toEqual([
      'Personas',
      'Agentes',
    ]);
    expect(el.querySelector('img')?.getAttribute('src')).toBe('https://avatars.example.com/ana');
    expect(el.querySelector('img')?.getAttribute('referrerpolicy')).toBe('no-referrer');

    items(el)[0].click();
    const req = http.expectOne('/api/0/issues/7/assignee');
    expect(req.request.method).toBe('POST');
    expect(req.request.body).toEqual({ assignee: 'dev', kind: 'user' });
    req.flush({ id: 7, assignee: { name: 'dev', kind: 'user', active: true } });
    expect(changed[0].assignee?.name).toBe('dev');
    expect(notices).toEqual(['Asignado a dev']);
  });

  it('asigna a un agente, filtra y quita la asignación', async () => {
    const { fixture, el, notices } = await render({
      name: 'ana',
      kind: 'user',
      display_name: 'Ana Pérez',
      active: true,
    });
    expect(text(el.querySelector('.trigger'))).toBe('A Ana Pérez');
    await open(fixture);
    // Ya asignada a otra: «Asignármelo» y «Quitar la asignación».
    expect(items(el).map(text).slice(0, 2)).toEqual(['Asignármelo', 'Quitar la asignación']);
    expect(items(el)[2].getAttribute('aria-checked')).toBe('true');

    const filter = el.querySelector<HTMLInputElement>('.picker__filter input');
    expect(filter?.getAttribute('placeholder')).toBe('Asignar a…');
    filter!.value = 'agen';
    filter!.dispatchEvent(new Event('input'));
    fixture.detectChanges();
    expect(items(el).map(text)).toEqual(['agente']);
    el.querySelector<HTMLFormElement>('.picker__filter')!.dispatchEvent(new Event('submit'));
    const req = http.expectOne('/api/0/issues/7/assignee');
    expect(req.request.body).toEqual({ assignee: 'agente', kind: 'token' });
    req.flush({ id: 7, assignee: { name: 'agente', kind: 'token', active: true } });

    fixture.componentRef.setInput('assignee', { name: 'agente', kind: 'token', active: true });
    await fixture.whenStable();
    expect(text(el.querySelector('.trigger__label'))).toBe('agente');
    expect(text(el.querySelector('.trigger__kind'))).toBe('token');
    expect(el.querySelector('.trigger app-avatar svg')).not.toBeNull();
    await open(fixture);
    items(el)
      .find((b) => text(b) === 'Quitar la asignación')
      ?.click();
    const del = http.expectOne('/api/0/issues/7/assignee');
    expect(del.request.method).toBe('DELETE');
    del.flush({ id: 7, assignee: null });
    expect(notices.at(-1)).toBe('Asignación quitada');
  });

  it('un token revocado se ve como tal', async () => {
    const { el } = await render({ name: 'viejo', kind: 'token', active: false });
    expect(text(el.querySelector('.trigger__label'))).toBe('viejo (token revocado)');
    expect(el.querySelector('app-avatar')?.classList).toContain('avatar--inactive');
  });

  it('en inglés', async () => {
    lang.set('en');
    const { fixture, el, notices } = await render({
      name: 'viejo',
      kind: 'token',
      active: false,
    });
    expect(text(el.querySelector('.trigger__label'))).toBe('viejo (revoked token)');
    expect(el.querySelector('.trigger')?.getAttribute('aria-label')).toBe(
      'Assignee: viejo (revoked token)',
    );
    await open(fixture);
    expect(items(el).map(text).slice(0, 2)).toEqual(['Assign to me', 'Unassign']);
    expect(Array.from(el.querySelectorAll('.menu__label')).map(text)).toEqual(['People', 'Agents']);
    expect(el.querySelector('.picker__filter input')?.getAttribute('placeholder')).toBe(
      'Assign to…',
    );
    expect(spanishLeft(el)).toEqual([]);
    items(el)[0].click();
    http
      .expectOne('/api/0/issues/7/assignee')
      .flush({ id: 7, assignee: { name: 'dev', kind: 'user', active: true } });
    expect(notices).toEqual(['Assigned to dev']);

    fixture.componentRef.setInput('assignee', null);
    await fixture.whenStable();
    expect(text(el.querySelector('.trigger'))).toBe('Unassigned');
    expect(spanishLeft(el)).toEqual([]);
  });
});
