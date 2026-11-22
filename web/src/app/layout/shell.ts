import { ChangeDetectionStrategy, Component, computed, inject, signal } from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { NavigationEnd, Router, RouterLink, RouterOutlet } from '@angular/router';
import { filter, map } from 'rxjs';
import { EnvironmentChoice } from '../core/environment';
import { I18n, type Key, LANGS, type Lang } from '../core/i18n';
import { Session } from '../core/session';
import { Theme, type ThemeChoice } from '../core/theme';
import { Icon } from '../ui/icon';

const THEMES: { value: ThemeChoice; label: Key; icon: string }[] = [
  { value: 'system', label: 'theme.system', icon: 'desktop' },
  { value: 'light', label: 'theme.light', icon: 'sun' },
  { value: 'dark', label: 'theme.dark', icon: 'moon' },
];

function sectionOf(url: string): 'issues' | 'releases' | 'settings' {
  if (url.startsWith('/releases')) {
    return 'releases';
  }
  if (url.startsWith('/settings') || /^\/projects\/[^/]+\/settings/.test(url)) {
    return 'settings';
  }
  return 'issues';
}

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
  protected readonly menu = signal<'env' | 'lang' | 'theme' | 'user' | null>(null);

  private readonly router = inject(Router);
  /** Qué sección del raíl se marca: releases, ajustes o, en el resto, los errores. */
  protected readonly section = toSignal(
    this.router.events.pipe(
      filter((e) => e instanceof NavigationEnd),
      map(() => sectionOf(this.router.url)),
    ),
    { initialValue: sectionOf(this.router.url) },
  );

  protected readonly env = inject(EnvironmentChoice);
  /** Los entornos de los eventos; el elegido sale aunque ya no tenga, para poder quitarlo. */
  protected readonly envNames = computed(() => {
    const names = this.env.list().map((e) => e.name);
    const current = this.env.value();
    return current && !names.includes(current) ? [...names, current] : names;
  });
  protected readonly envLabel = computed(() =>
    this.t('shell.environment', { env: this.env.value() || this.t('env.all') }),
  );

  constructor() {
    this.env.refresh();
  }

  protected toggleEnv(): void {
    // Al abrirlo se vuelve a pedir la lista, por si han llegado entornos nuevos.
    if (this.menu() !== 'env') {
      this.env.refresh();
    }
    this.toggle('env');
  }

  protected setEnv(value: string): void {
    this.env.set(value);
    this.menu.set(null);
  }

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

  protected toggle(menu: 'env' | 'lang' | 'theme' | 'user'): void {
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
