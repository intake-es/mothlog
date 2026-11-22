import { quote, replaceToken, setTerm, term, termValue, tokenAt, tokenize } from './search';

describe('búsqueda', () => {
  it('parte en términos con negación, comillas y texto libre', () => {
    const tokens = tokenize('is:open  !browser:Chrome os:"Mac OS" "by zero" https://a.es/x');
    expect(tokens.map((t) => [t.negate, t.key, t.value])).toEqual([
      [false, 'is', 'open'],
      [true, 'browser', 'Chrome'],
      [false, 'os', 'Mac OS'],
      [false, null, 'by zero'],
      [false, null, 'https://a.es/x'],
    ]);
    expect(tokens[2].raw).toBe('os:"Mac OS"');
    expect(tokenize('a:"con \\"comillas\\""')[0].value).toBe('con "comillas"');
    expect(tokenize('os:"sin cerrar')[0].value).toBe('sin cerrar');
  });

  it('pone comillas solo cuando hacen falta y vuelve al mismo valor', () => {
    for (const [v, q] of [
      ['Chrome', 'Chrome'],
      ['Mac OS', '"Mac OS"'],
      ['a"b', '"a\\"b"'],
      ['c:\\dir', '"c:\\\\dir"'],
      ['', '""'],
    ]) {
      expect(quote(v)).toBe(q);
      expect(tokenize(`k:${q}`)[0].value).toBe(v);
    }
    expect(term('browser', 'Chrome', true)).toBe('!browser:Chrome');
  });

  it('sabe qué término se escribe y lo cambia', () => {
    const q = 'is:open bro';
    const t = tokenAt(q, q.length);
    expect([t.key, t.value, t.start]).toEqual([null, 'bro', 8]);
    expect(replaceToken(q, t, 'browser:')).toEqual({ query: 'is:open browser:', caret: 16 });
    const v = tokenAt('is:open browser:Sa', 18);
    expect([v.key, v.value]).toEqual(['browser', 'Sa']);
    expect(replaceToken('is:open browser:Sa', v, 'browser:Safari').query).toBe(
      'is:open browser:Safari ',
    );
    expect(tokenAt('is:open ', 8).raw).toBe('');
  });

  it('las píldoras ponen o quitan su término sin tocar el resto', () => {
    expect(setTerm('is:open timeout', 'is', 'resolved')).toBe('is:resolved timeout');
    expect(setTerm('is:open timeout', 'is', null)).toBe('timeout');
    expect(setTerm('is:open', 'project', 'web')).toBe('project:web is:open');
    expect(setTerm('!project:web timeout', 'project', 'api')).toBe(
      'project:api !project:web timeout',
    );
    expect(setTerm('project:"mi app" x', 'project', 'mi app 2')).toBe('project:"mi app 2" x');
    expect(termValue('!is:open is:resolved', 'is')).toBe('resolved');
    expect(termValue('timeout', 'is')).toBeNull();
  });

  it('el filtro de estado no toca is:assigned ni is:unassigned', () => {
    expect(termValue('is:unassigned is:open', 'is')).toBe('open');
    expect(termValue('is:assigned', 'is')).toBeNull();
    expect(setTerm('is:unassigned is:open', 'is', 'resolved')).toBe('is:unassigned is:resolved');
    expect(setTerm('is:assigned is:open', 'is', null)).toBe('is:assigned');
  });

  it('«Míos» pone y quita assigned:me', () => {
    expect(setTerm('is:open', 'assigned', 'me')).toBe('assigned:me is:open');
    expect(termValue('assigned:me is:open', 'assigned')).toBe('me');
    expect(setTerm('assigned:me is:open !assigned:ana', 'assigned', null)).toBe(
      'is:open !assigned:ana',
    );
  });
});
