import { ChangeDetectionStrategy, Component, computed, inject, signal } from '@angular/core';
import { RouterLink, RouterOutlet } from '@angular/router';
import { I18n, type Key, LANGS, type Lang } from '../core/i18n';
import { Session } from '../core/session';
import { Theme, type ThemeChoice } from '../core/theme';
import { Icon } from '../ui/icon';

const THEMES: { value: ThemeChoice; label: Key; icon: string }[] = [
  { value: 'system', label: 'theme.system', icon: 'desktop' },
  { value: 'light', label: 'theme.light', icon: 'sun' },
  { value: 'dark', label: 'theme.dark', icon: 'moon' },
];

/** El raíl fino a la izquierda (arriba en el móvil) y el panel blanco con la pantalla. */
@Component({
  selector: 'app-shell',
  imports: [RouterOutlet, RouterLink, Icon],
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { '(keydown.escape)': 'menu.set(null)' },
  templateUrl: './shell.html',
  styleUrl: './shell.css',
})
export class Shell {
  protected readonly session = inject(Session);
  protected readonly theme = inject(Theme);
  protected readonly i18n = inject(I18n);
  protected readonly t = this.i18n.t;
  protected readonly themes = THEMES;
  protected readonly langs = LANGS;
  protected readonly menu = signal<'lang' | 'theme' | 'user' | null>(null);

  protected readonly user = computed(() => this.session.info()?.user ?? null);
  protected readonly initials = computed(() => {
    const u = this.user();
    const words = (u?.name || u?.login || '?').trim().split(/\s+/);
    return words
      .slice(0, 2)
      .map((w) => w[0]?.toUpperCase() ?? '')
      .join('');
  });
  protected readonly themeIcon = computed(
    () => THEMES.find((t) => t.value === this.theme.choice())?.icon ?? 'desktop',
  );

  protected toggle(menu: 'lang' | 'theme' | 'user'): void {
    this.menu.set(this.menu() === menu ? null : menu);
  }

  protected setTheme(choice: ThemeChoice): void {
    this.theme.set(choice);
    this.menu.set(null);
  }

  protected setLang(lang: Lang): void {
    this.i18n.set(lang);
    this.menu.set(null);
  }

  protected logout(): void {
    this.menu.set(null);
    void this.session.logout();
  }
}
