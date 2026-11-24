import { ChangeDetectionStrategy, Component, computed, inject, signal } from '@angular/core';
import { apiError } from '../../core/api';
import { ago, fullDate } from '../../core/format';
import { I18n, type Key } from '../../core/i18n';
import { OAuthApi, type OAuthGrant, SCOPES, type Scope } from '../../core/oauth';
import { AppMark } from '../../ui/app-mark';
import { ConfirmDialog } from '../../ui/confirm-dialog';
import { Icon } from '../../ui/icon';

const SCOPE_LABEL: Record<Scope, Key> = {
  read: 'consent.scope.read',
  write: 'consent.scope.write',
};

interface Row {
  g: OAuthGrant;
  scopes: { value: Scope; label: Key }[];
  connected: string;
  connectedTitle: string;
  used: string;
  usedTitle: string;
  host: string;
}

function hostOf(uri: string): string {
  try {
    return new URL(uri).host;
  } catch {
    return uri;
  }
}

/** Las aplicaciones a las que la persona ha dado acceso al MCP, y revocarlas. */
@Component({
  selector: 'app-connected-apps',
  imports: [Icon, AppMark, ConfirmDialog],
  changeDetection: ChangeDetectionStrategy.OnPush,
  templateUrl: './connected-apps.html',
  styleUrl: './connected-apps.css',
})
export class ConnectedAppsPage {
  private readonly api = inject(OAuthApi);
  protected readonly t = inject(I18n).t;

  protected readonly grants = signal<OAuthGrant[] | null>(null);
  private readonly failure = signal<unknown>(null);
  protected readonly error = computed(() =>
    this.failure() === null ? '' : apiError(this.failure()),
  );
  /** La que se va a revocar, mientras está abierto el modal. */
  protected readonly confirming = signal<OAuthGrant | null>(null);
  protected readonly revoking = signal(false);
  protected readonly revokeError = signal('');

  protected readonly rows = computed<Row[]>(() =>
    (this.grants() ?? []).map((g) => ({
      g,
      scopes: SCOPES.filter((s) => g.scopes.includes(s)).map((s) => ({
        value: s,
        label: SCOPE_LABEL[s],
      })),
      connected: this.t('apps.connected', { when: ago(g.created_at) }),
      connectedTitle: fullDate(g.created_at),
      used: g.last_used_at
        ? this.t('apps.lastUsed', { when: ago(g.last_used_at), ip: g.last_used_ip ?? '' })
        : this.t('apps.neverUsed'),
      usedTitle: g.last_used_at ? fullDate(g.last_used_at) : '',
      host: this.t('apps.redirect', { host: hostOf(g.redirect_uri) }),
    })),
  );

  constructor() {
    this.load();
  }

  protected load(): void {
    this.failure.set(null);
    this.grants.set(null);
    this.api.grants().subscribe({
      next: (page) => this.grants.set(page.data),
      error: (err) => {
        this.failure.set(err);
        this.grants.set([]);
      },
    });
  }

  protected ask(g: OAuthGrant): void {
    this.revokeError.set('');
    this.confirming.set(g);
  }

  protected close(): void {
    this.confirming.set(null);
  }

  protected revoke(): void {
    const g = this.confirming();
    if (!g) {
      return;
    }
    this.revoking.set(true);
    this.revokeError.set('');
    this.api.revoke(g.id).subscribe({
      next: () => this.done(g),
      error: (err: { status?: number }) => {
        // Si ya no existe, ya no tiene acceso: se quita igual.
        if (err?.status === 404) {
          this.done(g);
          return;
        }
        this.revoking.set(false);
        this.revokeError.set(this.t('apps.revokeFailed'));
      },
    });
  }

  private done(g: OAuthGrant): void {
    this.revoking.set(false);
    this.confirming.set(null);
    this.grants.set((this.grants() ?? []).filter((x) => x.id !== g.id));
  }
}
