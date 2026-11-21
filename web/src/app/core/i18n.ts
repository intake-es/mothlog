import { Injectable, signal } from '@angular/core';
import { en } from './i18n/en';
import { es, type Key } from './i18n/es';

export type { Key } from './i18n/es';
export type Lang = 'es' | 'en';

export const LANGS: { value: Lang; label: string }[] = [
  { value: 'es', label: 'Español' },
  { value: 'en', label: 'English' },
];

export const DICTIONARIES: Record<Lang, Record<Key, string>> = { es, en };

const KEY = 'ie-lang';

function browserLanguage(): string {
  return typeof navigator === 'undefined' ? '' : (navigator.language ?? '');
}

/** El elegido a mano; si no, español si el navegador empieza por «es» e inglés en el resto. */
export function initialLang(): Lang {
  try {
    const v = localStorage.getItem(KEY);
    if (v === 'es' || v === 'en') {
      return v;
    }
  } catch {
    // Sin localStorage: el del navegador.
  }
  return browserLanguage().toLowerCase().startsWith('es') ? 'es' : 'en';
}

/** El idioma de toda la app: lo leen las plantillas, los computed y los formatos de format.ts. */
export const lang = signal<Lang>(initialLang());

/** La región del navegador si es del mismo idioma (en-GB, es-MX…); si no, una por defecto. */
export function locale(l: Lang = lang()): string {
  const nav = browserLanguage();
  return nav.toLowerCase().startsWith(l) ? nav : l === 'es' ? 'es-ES' : 'en-US';
}

export type Params = Record<string, string | number>;

export function t(key: Key, params?: Params): string {
  const text = DICTIONARIES[lang()][key];
  return params ? text.replace(/\{(\w+)\}/g, (m, name) => String(params[name] ?? m)) : text;
}

/** «1 evento», «3 eventos»: la clave `<base>.one` o `<base>.other` con {n} ya formateado. */
export function plural(base: string, n: number, params?: Params): string {
  const key = `${base}.${n === 1 ? 'one' : 'other'}` as Key;
  return t(key, { ...params, n: new Intl.NumberFormat(locale()).format(n) });
}

/**
 * El texto alrededor de un hueco que la plantilla pinta con su propio marcado:
 * «Entra con tu cuenta de GitHub de {org}.» da before y after de {org}.
 */
export function around(key: Key, slot: string): { before: string; after: string } {
  const text = t(key);
  const at = text.indexOf(`{${slot}}`);
  return at < 0
    ? { before: text, after: '' }
    : { before: text.slice(0, at), after: text.slice(at + slot.length + 2) };
}

export function applyLang(l: Lang, root: HTMLElement = document.documentElement): void {
  root.lang = l;
}

@Injectable({ providedIn: 'root' })
export class I18n {
  readonly lang = lang.asReadonly();
  readonly t = t;
  readonly plural = plural;
  readonly around = around;

  constructor() {
    applyLang(lang());
  }

  set(l: Lang): void {
    lang.set(l);
    try {
      localStorage.setItem(KEY, l);
    } catch {
      // Sin localStorage vale para esta pestaña.
    }
    applyLang(l);
  }
}
