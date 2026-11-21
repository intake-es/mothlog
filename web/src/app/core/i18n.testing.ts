import { DICTIONARIES } from './i18n';

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/**
 * Los textos del diccionario español que siguen en pantalla con la app en inglés:
 * en el texto o en aria-label, title y placeholder. Solo cuentan los que cambian
 * entre idiomas; de los que llevan huecos, el trozo fijo más largo.
 */
export function spanishLeft(el: Element): string[] {
  const attrs = Array.from(el.querySelectorAll('[aria-label], [title], [placeholder]')).flatMap(
    (n) => ['aria-label', 'title', 'placeholder'].map((a) => n.getAttribute(a) ?? ''),
  );
  const haystack = [el.textContent ?? '', el.getAttribute('aria-label') ?? '', ...attrs].join('\n');
  const english = new Set(Object.values(DICTIONARIES.en));
  const left = new Set<string>();
  for (const value of Object.values(DICTIONARIES.es)) {
    if (english.has(value)) {
      continue;
    }
    const fixed = value
      .split(/\{\w+\}/)
      .map((s) => s.trim())
      .sort((a, b) => b.length - a.length)[0];
    if (!fixed || (value.includes('{') && fixed.length < 6)) {
      continue;
    }
    if (new RegExp(`(?<!\\p{L})${escapeRegExp(fixed)}(?!\\p{L})`, 'u').test(haystack)) {
      left.add(fixed);
    }
  }
  return [...left];
}
