import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core';
import { around, I18n, type Key, locale } from '../../core/i18n';
import { Session } from '../../core/session';
import { Icon } from '../../ui/icon';
import { Logo } from '../../ui/logo';

const ERRORS: Record<string, Key> = {
  org: 'login.error.org',
  state: 'login.error.state',
  denied: 'login.error.denied',
  github: 'login.error.github',
  config: 'login.error.config',
  oidc_config: 'login.error.oidcConfig',
  domain: 'login.error.domain',
  group: 'login.error.group',
  oidc: 'login.error.oidc',
  oidc_denied: 'login.error.oidcDenied',
  save: 'login.error.save',
  dev_key: 'login.error.devKey',
};

const SERVER_VARS = ['GITHUB_CLIENT_ID', 'GITHUB_CLIENT_SECRET', 'GITHUB_ORG', 'SESSION_SECRET'];
const OIDC_LIMITS = ['OIDC_ALLOWED_DOMAINS', 'OIDC_ALLOWED_GROUPS'];
const LIMITS_SLOT = OIDC_LIMITS.join();

@Component({
  selector: 'app-login',
  imports: [Icon, Logo],
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
  protected readonly oidc = computed(() => this.info()?.methods.includes('oidc') ?? false);
  protected readonly oidcName = computed(() => this.info()?.oidc_name ?? '');
  protected readonly dev = computed(() => this.info()?.methods.includes('dev') ?? false);
  protected readonly configured = computed(() => this.github() || this.oidc() || this.dev());
  protected readonly message = computed(() => {
    const e = this.error();
    return e ? this.t(ERRORS[e] ?? 'login.error.other', { name: this.oidcName() }) : '';
  });
  protected readonly orgLede = computed(() => around('login.lede.github', 'org'));
  protected readonly oidcLede = computed(() => around('login.lede.oidc', 'name'));
  /** Las variables que faltan, en <code> y unidas como se enumera en cada idioma. */
  protected readonly missing = computed(() => ({
    ...around('login.off.text', 'vars'),
    vars: new Intl.ListFormat(locale(), { type: 'conjunction' }).formatToParts(SERVER_VARS),
  }));
  /** Lo que hace falta para OIDC: el emisor y el cliente, y uno de los dos límites. */
  protected readonly missingOidc = computed(() => ({
    ...around('login.off.oidc', 'vars'),
    vars: new Intl.ListFormat(locale(), { type: 'conjunction' })
      .formatToParts(['OIDC_ISSUER', 'OIDC_CLIENT_ID', LIMITS_SLOT])
      .flatMap((p) =>
        p.value === LIMITS_SLOT
          ? new Intl.ListFormat(locale(), { type: 'disjunction' }).formatToParts(OIDC_LIMITS)
          : [p],
      ),
  }));

  constructor() {
    void this.session.load();
  }

  protected href(path: string): string {
    const next = this.next();
    return next ? `${path}?next=${encodeURIComponent(next)}` : path;
  }
}
