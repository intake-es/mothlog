import { ago, day, splitTitle } from './format';

describe('format', () => {
  const now = Date.parse('2026-11-19T12:00:00Z');

  it('dice hace cuánto', () => {
    expect(ago('2026-11-19T11:59:30Z', now)).toBe('ahora');
    expect(ago('2026-11-19T11:55:00Z', now)).toBe('hace 5 min');
    expect(ago('2026-11-19T09:00:00Z', now)).toBe('hace 3 h');
    expect(ago('2026-11-15T12:00:00Z', now)).toBe('hace 4 d');
    expect(ago('2026-09-01T12:00:00Z', now)).toContain('2026');
    expect(ago(null, now)).toBe('—');
  });

  it('separa el tipo del mensaje', () => {
    expect(splitTitle('TypeError: Cannot read properties', 'TypeError')).toEqual({
      type: 'TypeError',
      message: 'Cannot read properties',
    });
    expect(splitTitle('ai.action_reported_failure', '')).toEqual({
      type: '',
      message: 'ai.action_reported_failure',
    });
    expect(splitTitle('KeyError', 'KeyError')).toEqual({ type: 'KeyError', message: '' });
  });

  it('pinta un día de la serie en UTC', () => {
    expect(day('2026-11-19')).toBe('19 nov');
  });
});
