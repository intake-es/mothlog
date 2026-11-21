import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core';
import { around, I18n, type Key, locale } from '../../core/i18n';
import { Session } from '../../core/session';
import { Icon } from '../../ui/icon';

const ERRORS: Record<string, Key> = {
  org: 'login.error.org',
  state: 'login.error.state',
  denied: 'login.error.denied',
  github: 'login.error.github',
  config: 'login.error.config',
};

const SERVER_VARS = ['GITHUB_CLIENT_ID', 'GITHUB_CLIENT_SECRET', 'GITHUB_ORG', 'SESSION_SECRET'];

@Component({
  selector: 'app-login',
  imports: [Icon],
  changeDetection: ChangeDetectionStrategy.OnPush,
  templateUrl: './login.html',
  styleUrl: './login.css',
})
export class LoginPage {
  readonly error = input<string>();
  readonly next = input<string>();

  private readonly session = inject(Session);
  protected readonly t = inject(I18n).t;

  protected readonly info = this.session.info;
  protected readonly loaded = computed(() => this.info() !== null);
  protected readonly github = computed(() => this.info()?.methods.includes('github') ?? false);
  protected readonly dev = computed(() => this.info()?.methods.includes('dev') ?? false);
  protected readonly configured = computed(() => this.github() || this.dev());
  protected readonly message = computed(() => {
    const e = this.error();
    return e ? this.t(ERRORS[e] ?? 'login.error.other') : '';
  });
  protected readonly orgLede = computed(() => around('login.lede.github', 'org'));
  /** Las variables que faltan, en <code> y unidas como se enumera en cada idioma. */
  protected readonly missing = computed(() => ({
    ...around('login.off.text', 'vars'),
    vars: new Intl.ListFormat(locale(), { type: 'conjunction' }).formatToParts(SERVER_VARS),
  }));

  constructor() {
    void this.session.load();
  }

  protected href(path: string): string {
    const next = this.next();
    return next ? `${path}?next=${encodeURIComponent(next)}` : path;
  }
}
