import { Injectable, signal } from '@angular/core';

export type ThemeChoice = 'system' | 'light' | 'dark';

const KEY = 'ie-theme';

function stored(): ThemeChoice {
  try {
    const v = localStorage.getItem(KEY);
    return v === 'light' || v === 'dark' ? v : 'system';
  } catch {
    return 'system';
  }
}

/** Pone html.light o html.dark si se eligió a mano; sin clase, el CSS sigue al sistema. */
export function applyTheme(
  choice: ThemeChoice,
  root: HTMLElement = document.documentElement,
): void {
  root.classList.toggle('light', choice === 'light');
  root.classList.toggle('dark', choice === 'dark');
}

@Injectable({ providedIn: 'root' })
export class Theme {
  readonly choice = signal<ThemeChoice>(stored());

  constructor() {
    applyTheme(this.choice());
  }

  set(choice: ThemeChoice): void {
    this.choice.set(choice);
    try {
      if (choice === 'system') {
        localStorage.removeItem(KEY);
      } else {
        localStorage.setItem(KEY, choice);
      }
    } catch {
      // Sin localStorage (modo privado estricto) vale para esta pestaña.
    }
    applyTheme(choice);
  }
}

export const initialTheme = stored;
