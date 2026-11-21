import { effect, Injectable, inject, signal } from '@angular/core';
import { Title } from '@angular/platform-browser';
import { type RouterStateSnapshot, TitleStrategy } from '@angular/router';
import { type Key, t } from './i18n';

/**
 * El título de la pestaña: la clave de la ruta, o el que ponga la pantalla
 * (el del error abierto). Se vuelve a escribir al cambiar de idioma.
 */
@Injectable({ providedIn: 'root' })
export class PageTitle extends TitleStrategy {
  private readonly title = inject(Title);
  private readonly route = signal<Key | null>(null);
  private readonly custom = signal<(() => string) | null>(null);

  constructor() {
    super();
    effect(() => {
      const custom = this.custom();
      const key = this.route();
      const text = custom ? custom() : key ? t(key) : '';
      if (text) {
        this.title.setTitle(text);
      }
    });
  }

  override updateTitle(snapshot: RouterStateSnapshot): void {
    this.custom.set(null);
    this.route.set((this.buildTitle(snapshot) as Key | undefined) ?? null);
  }

  /** Un título que depende de la pantalla; se lee dentro de un effect, así que puede usar t(). */
  set(title: (() => string) | null): void {
    this.custom.set(title);
  }
}
