import { HttpErrorResponse } from '@angular/common/http';
import {
  ChangeDetectionStrategy,
  Component,
  computed,
  effect,
  inject,
  input,
  signal,
  untracked,
} from '@angular/core';
import { around, I18n, type Key } from '../../core/i18n';
import { Browser, OAuthApi, type OAuthRequest, SCOPES, type Scope } from '../../core/oauth';
import { Session } from '../../core/session';
import { AppMark } from '../../ui/app-mark';
import { Icon } from '../../ui/icon';

const ERRORS: Record<string, Key> = {
  client: 'consent.error.client',
  redirect: 'consent.error.redirect',
  server: 'consent.error.server',
};

const SCOPE_TEXT: Record<Scope, { label: Key; hint: Key; icon: string }> = {
  read: { label: 'consent.scope.read', hint: 'consent.scope.readHint', icon: 'eye' },
  write: {
    label: 'consent.scope.write',
    hint: 'consent.scope.writeHint',
    icon: 'pencil-simple-line',
  },
};

type State = 'loading' | 'ready' | 'gone' | 'failed' | 'leaving';

/**
 * La pantalla donde la persona deja a una aplicación usar el MCP con su
 * cuenta: /authorize?request=…, a la que manda /oauth/authorize. Leer sale
 * marcado y escribir no; se concede lo marcado.
 */
@Component({
  selector: 'app-authorize',
  imports: [Icon, AppMark],
  changeDetection: ChangeDetectionStrategy.OnPush,
  templateUrl: './authorize.html',
  styleUrl: './authorize.css',
})
export class AuthorizePage {
  readonly request = input<string>();
  readonly error = input<string>();

  private readonly api = inject(OAuthApi);
  private readonly browser = inject(Browser);
  private readonly session = inject(Session);
  protected readonly t = inject(I18n).t;

  protected readonly state = signal<State>('loading');
  protected readonly req = signal<OAuthRequest | null>(null);
  protected readonly chosen = signal<ReadonlySet<Scope>>(new Set(['read']));
  protected readonly sending = signal(false);
  protected readonly sendError = signal('');

  protected readonly login = computed(() => this.session.info()?.user?.login ?? '');
  protected readonly problem = computed(() => {
    const e = this.error();
    if (e) {
      return this.t(ERRORS[e] ?? 'consent.error.other');
    }
    return this.request() ? '' : this.t('consent.error.other');
  });
  protected readonly scopes = computed(() =>
    SCOPES.filter((s) => this.req()?.scopes.includes(s)).map((s) => ({
      value: s,
      ...SCOPE_TEXT[s],
    })),
  );
  protected readonly lede = computed(() => around('consent.lede', 'client'));
  protected readonly note = computed(() =>
    this.t('consent.note', { login: this.login(), client: this.req()?.client_name ?? '' }),
  );
  protected readonly redirectHost = computed(() => {
    try {
      return new URL(this.req()?.redirect_uri ?? '').host;
    } catch {
      return '';
    }
  });

  constructor() {
    void this.session.load();
    effect(() => {
      const id = this.request();
      if (id && !this.error()) {
        untracked(() => this.load(id));
      }
    });
  }

  private load(id: string): void {
    this.state.set('loading');
    this.api.request(id).subscribe({
      next: (r) => {
        this.req.set(r);
        this.chosen.set(new Set(r.scopes.includes('read') ? ['read'] : []));
        this.state.set('ready');
      },
      error: (err: unknown) =>
        this.state.set(err instanceof HttpErrorResponse && err.status === 404 ? 'gone' : 'failed'),
    });
  }

  protected toggle(scope: Scope, on: boolean): void {
    const next = new Set(this.chosen());
    if (on) {
      next.add(scope);
    } else {
      next.delete(scope);
    }
    this.chosen.set(next);
  }

  protected allow(): void {
    const id = this.request();
    const scopes = SCOPES.filter((s) => this.chosen().has(s));
    if (!id || scopes.length === 0) {
      return;
    }
    this.answer(this.api.approve(id, scopes));
  }

  protected deny(): void {
    const id = this.request();
    if (id) {
      this.answer(this.api.deny(id));
    }
  }

  private answer(call: ReturnType<OAuthApi['deny']>): void {
    this.sending.set(true);
    this.sendError.set('');
    call.subscribe({
      next: ({ redirect_to }) => {
        this.state.set('leaving');
        this.browser.go(redirect_to);
      },
      error: (err: unknown) => {
        this.sending.set(false);
        if (err instanceof HttpErrorResponse && err.status === 404) {
          this.state.set('gone');
        } else {
          this.sendError.set(this.t('consent.failed'));
        }
      },
    });
  }
}
