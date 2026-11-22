import { Injectable, inject, signal } from '@angular/core';
import { Api, type Environment } from './api';

const KEY = 'ie-env';

function initialEnvironment(): string {
  try {
    return localStorage.getItem(KEY) ?? '';
  } catch {
    return '';
  }
}

/** El entorno elegido para toda la app; vacío, todos. Lo leen la lista, el detalle y las releases. */
export const environment = signal<string>(initialEnvironment());

@Injectable({ providedIn: 'root' })
export class EnvironmentChoice {
  private readonly api = inject(Api);
  readonly value = environment.asReadonly();
  /** Los entornos que salen en los eventos, del visto más recientemente al que menos. */
  readonly list = signal<Environment[]>([]);

  refresh(): void {
    this.api.environments().subscribe({
      next: (page) => this.list.set(page.data),
      error: () => this.list.set([]),
    });
  }

  set(value: string): void {
    environment.set(value);
    try {
      if (value) {
        localStorage.setItem(KEY, value);
      } else {
        localStorage.removeItem(KEY);
      }
    } catch {
      // Sin localStorage vale para esta pestaña.
    }
  }
}
