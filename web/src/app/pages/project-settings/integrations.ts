import { ChangeDetectionStrategy, Component, computed, inject, signal } from '@angular/core';
import { Api, apiError, type IntegrationStatus, type Integrations } from '../../core/api';
import { copyText } from '../../core/clipboard';
import { ago, fullDate } from '../../core/format';
import { I18n, type Key } from '../../core/i18n';
import { Icon } from '../../ui/icon';

interface Provider {
  id: 'linear' | 'github';
  name: string;
  icon: string;
  secretEnv: string;
  tokenEnv: string;
  steps: Key;
}

const PROVIDERS: Provider[] = [
  {
    id: 'linear',
    name: 'Linear',
    icon: 'kanban',
    secretEnv: 'LINEAR_WEBHOOK_SECRET',
    tokenEnv: 'LINEAR_API_KEY',
    steps: 'integrations.linearSteps',
  },
  {
    id: 'github',
    name: 'GitHub',
    icon: 'github-logo',
    secretEnv: 'GITHUB_WEBHOOK_SECRET',
    tokenEnv: 'GITHUB_TOKEN',
    steps: 'integrations.githubSteps',
  },
];

interface Card {
  p: Provider;
  s: IntegrationStatus;
  badge: string;
  status: Key;
  last: string;
  lastTitle: string;
}

/** Las URL de los webhooks de Linear y GitHub y si el servidor tiene lo que hace falta. */
@Component({
  selector: 'app-integrations',
  imports: [Icon],
  changeDetection: ChangeDetectionStrategy.OnPush,
  templateUrl: './integrations.html',
  styleUrls: ['./settings.css', './integrations.css'],
})
export class IntegrationsSection {
  private readonly api = inject(Api);
  protected readonly t = inject(I18n).t;

  protected readonly data = signal<Integrations | null>(null);
  private readonly failure = signal<unknown>(null);
  protected readonly error = computed(() =>
    this.failure() === null ? '' : apiError(this.failure()),
  );
  protected readonly copied = signal<string>('');

  protected readonly cards = computed<Card[]>(() => {
    const d = this.data();
    if (!d) {
      return [];
    }
    return PROVIDERS.map((p) => {
      const s = d[p.id];
      const ready = s.secret_set && s.token_set;
      return {
        p,
        s,
        badge: ready
          ? 'sp-badge--green'
          : s.secret_set || s.token_set
            ? 'sp-badge--yellow'
            : 'sp-badge--gray',
        status: ready
          ? 'integrations.ready'
          : s.secret_set || s.token_set
            ? 'integrations.partial'
            : 'integrations.off',
        last: s.last_delivery ? ago(s.last_delivery) : '',
        lastTitle: s.last_delivery ? fullDate(s.last_delivery) : '',
      };
    });
  });

  constructor() {
    this.load();
  }

  protected load(): void {
    this.failure.set(null);
    this.api.integrations().subscribe({
      next: (d) => this.data.set(d),
      error: (err) => this.failure.set(err),
    });
  }

  protected async copy(c: Card): Promise<void> {
    try {
      await copyText(Promise.resolve(c.s.webhook_url));
      this.copied.set(c.p.id);
      setTimeout(() => this.copied.set(''), 2000);
    } catch {
      this.copied.set('');
    }
  }
}
