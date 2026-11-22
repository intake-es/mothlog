import { TestBed } from '@angular/core/testing';
import type { IssueLink } from '../../core/api';
import { lang } from '../../core/i18n';
import { spanishLeft } from '../../core/i18n.testing';
import { IssueLinks } from './issue-links';

const at = new Date(Date.now() - 5 * 60_000).toISOString();

const link = (over: Partial<IssueLink>): IssueLink => ({
  id: 1,
  issue_id: 12,
  provider: 'linear',
  kind: 'issue',
  external_id: '6f1c2b8e-1d2a-4f00-9a7e-3c1b2a1d0e9f',
  identifier: 'ERR-7',
  url: 'https://linear.app/ejemplo/issue/ERR-7',
  state: 'closed',
  state_name: 'Done',
  created_at: at,
  updated_at: at,
  ...over,
});

const LINKS: IssueLink[] = [
  link({}),
  link({
    id: 2,
    provider: 'github',
    external_id: 'ejemplo/backend#3',
    identifier: 'ejemplo/backend#3',
    url: 'https://github.com/ejemplo/backend/issues/3',
    state: 'open',
    state_name: 'open',
  }),
  link({
    id: 3,
    provider: 'github',
    kind: 'pull_request',
    external_id: 'ejemplo/backend#9',
    identifier: 'ejemplo/backend#9',
    url: 'https://github.com/ejemplo/backend/pull/9',
    state: 'merged',
    state_name: 'merged',
  }),
  link({ id: 4, identifier: 'ERR-2', url: '', state: '', state_name: '' }),
];

function render(links: IssueLink[]) {
  const fixture = TestBed.createComponent(IssueLinks);
  fixture.componentRef.setInput('links', links);
  fixture.detectChanges();
  return { fixture, el: fixture.nativeElement as HTMLElement };
}

describe('IssueLinks', () => {
  afterEach(() => lang.set('es'));

  it('pinta cada enlace con su sitio, su estado y el enlace que abre en otra pestaña', () => {
    lang.set('es');
    const { el } = render(LINKS);
    const rows = el.querySelectorAll('.link');
    expect(rows).toHaveLength(4);
    const a = rows[0].querySelector<HTMLAnchorElement>('a.link__name')!;
    expect(a.textContent?.trim()).toBe('ERR-7');
    expect(a.href).toBe('https://linear.app/ejemplo/issue/ERR-7');
    expect(a.target).toBe('_blank');
    expect(a.rel).toContain('noopener');
    expect(a.getAttribute('aria-label')).toBe('Abrir ERR-7 en Linear');
    expect(rows[0].querySelector('.sp-badge')?.textContent).toBe('Cerrada');
    expect(rows[0].querySelector('.link__meta')?.textContent).toContain('Issue de Linear');
    expect(rows[0].querySelector('.link__meta')?.textContent).toContain('Done');
    expect(rows[1].querySelector('.sp-badge')?.textContent).toBe('Abierta');
    expect(rows[1].querySelector('.link__meta')?.textContent).toContain('Issue de GitHub');
    // «open» no dice más que «Abierta».
    expect(rows[1].querySelector('.link__meta')?.textContent).not.toContain('open');
    expect(rows[2].querySelector('.sp-badge')?.textContent).toBe('Mergeado');
    expect(rows[2].querySelector('.link__meta')?.textContent).toContain('PR de GitHub');
    expect(rows[3].querySelector('a')).toBeNull();
    expect(rows[3].querySelector('.sp-badge')?.textContent).toBe('Sin estado');
  });

  it('sin enlaces explica de dónde salen', () => {
    lang.set('es');
    const { el } = render([]);
    expect(el.querySelector('.empty')?.textContent).toContain('Sin issues ni PRs enlazados');
  });

  it('en inglés', () => {
    lang.set('en');
    const { el } = render(LINKS);
    const rows = el.querySelectorAll('.link');
    expect(rows[0].querySelector('.sp-badge')?.textContent).toBe('Closed');
    expect(rows[0].querySelector('a')?.getAttribute('aria-label')).toBe('Open ERR-7 in Linear');
    expect(rows[2].querySelector('.link__meta')?.textContent).toContain('GitHub pull request');
    expect(rows[3].querySelector('.sp-badge')?.textContent).toBe('No state');
    expect(spanishLeft(el)).toEqual([]);
    const empty = render([]).el;
    expect(empty.textContent).toContain('No linked issues or pull requests');
    expect(spanishLeft(empty)).toEqual([]);
  });
});
