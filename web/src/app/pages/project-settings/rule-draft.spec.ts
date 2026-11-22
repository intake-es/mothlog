import type { Rule } from '../../core/api';
import { ACTION_FIELDS, draftOf, emptyAction, inputOf, newDraft } from './rule-draft';

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
