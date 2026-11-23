import type { Rule } from '../../core/api';
import {
  ACTION_FIELDS,
  draftOf,
  emptyAction,
  inputOf,
  newDraft,
  secretAgain,
  withField,
} from './rule-draft';

describe('rule-draft con GitHub', () => {
  it('manda el repositorio, las etiquetas de GitHub y el token, y no los ids de Linear', () => {
    const d = newDraft();
    d.name = 'A GitHub';
    d.actions = [
      {
        ...emptyAction('github'),
        id: 'gh',
        repo: ' ejemplo/backend ',
        labels: 'bug, errores',
        token: '',
      },
    ];
    const input = inputOf(d);
    expect(input.actions).toEqual([
      { id: 'gh', type: 'github', repo: 'ejemplo/backend', labels: ['bug', 'errores'], token: '' },
    ]);
    expect(ACTION_FIELDS.github.find((f) => f.field === 'token')?.secret).toBe(true);
  });

  it('vuelve a la regla guardada con sus etiquetas', () => {
    const r: Rule = {
      id: 1,
      project_id: 4,
      name: 'A GitHub',
      enabled: true,
      conditions: {
        new: true,
        regressed: false,
        frequency: null,
        users: null,
        environments: [],
        levels: [],
        tags: [],
      },
      actions: [
        { id: 'gh', type: 'github', repo: 'ejemplo/backend', labels: ['bug'], token: '…wxyz' },
        { id: 'lin', type: 'linear', label_ids: ['label-1'] },
      ],
      throttle_minutes: 0,
    };
    const d = draftOf(r);
    expect(d.actions[0]).toMatchObject({ repo: 'ejemplo/backend', labels: 'bug', token: '…wxyz' });
    expect(d.actions[1]).toMatchObject({ labels: 'label-1' });
    expect(inputOf(d).actions[1]).toEqual({
      id: 'lin',
      type: 'linear',
      team_id: '',
      label_ids: ['label-1'],
    });
  });
});

describe('rule-draft al cambiar la URL', () => {
  const saved = { id: 'n', type: 'ntfy' as const, url: 'https://ntfy.sh/…opic', token: '…7654' };

  it('pide otra vez el token de ntfy si cambia la URL y lo recupera si vuelve', () => {
    const a = { ...emptyAction('ntfy'), id: 'n', url: saved.url, token: saved.token };
    const moved = withField(a, 'url', 'https://otro.example.com/tema', saved);
    expect(moved.token).toBe('');
    expect(secretAgain(moved, 'token', saved)).toBe('editor.tokenAgain');
    expect(inputOf({ ...newDraft(), actions: [moved] }).actions[0].token).toBe('');
    const back = withField(moved, 'url', saved.url, saved);
    expect(back.token).toBe(saved.token);
    expect(secretAgain(back, 'token', saved)).toBeNull();
  });

  it('no toca el secreto si se cambia otro campo o es una acción nueva', () => {
    const a = { ...emptyAction('webhook'), id: 'w', url: 'https://a.example.com', secret: '…a1b2' };
    const hook = { id: 'w', type: 'webhook' as const, url: a.url, secret: a.secret };
    expect(withField(a, 'secret', 'nuevo', hook).secret).toBe('nuevo');
    expect(withField(a, 'url', 'https://b.example.com', undefined).secret).toBe('…a1b2');
    const moved = withField(a, 'url', 'https://b.example.com', hook);
    expect(moved.secret).toBe('');
    expect(secretAgain(moved, 'secret', hook)).toBe('editor.secretAgain');
  });
});
