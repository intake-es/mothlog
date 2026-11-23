import {
  ChangeDetectionStrategy,
  Component,
  computed,
  DestroyRef,
  effect,
  inject,
  input,
  signal,
  untracked,
} from '@angular/core';
import { DomSanitizer, type SafeHtml } from '@angular/platform-browser';
import type { Subscription } from 'rxjs';
import {
  type ActionType,
  Api,
  apiError,
  type DigestConfig,
  type DigestDestinationType,
  type DigestFrequency,
  type DigestPreview,
  type DigestResult,
  type RuleAction,
} from '../../core/api';
import { I18n, type Key, LANGS, locale } from '../../core/i18n';
import { Icon } from '../../ui/icon';
import { Pick, type PickOption } from '../../ui/pick';
import {
  ACTION_FIELDS,
  ACTION_HINT,
  ACTION_ICON,
  type ActionDraft,
  type ActionField,
  emptyAction,
  secretAgain,
  withField,
} from './rule-draft';

const TYPES: DigestDestinationType[] = ['email', 'slack', 'webhook'];
/** De lunes a domingo, con el número que usa el servidor (0 es domingo). */
const WEEKDAYS = [1, 2, 3, 4, 5, 6, 0];

interface DigestDraft {
  enabled: boolean;
  frequency: DigestFrequency;
  weekday: number;
  hour: number;
  timezone: string;
  language: 'es' | 'en';
  sendEmpty: boolean;
  destinations: ActionDraft[];
}

function browserZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  } catch {
    return 'UTC';
  }
}

function zones(): string[] {
  try {
    return Intl.supportedValuesOf('timeZone');
  } catch {
    return [];
  }
}

function list(s: string): string[] {
  return s
    .split(',')
    .map((v) => v.trim())
    .filter(Boolean);
}

/** El borrador de lo guardado; sin guardar nunca, con la zona y el idioma de quien lo abre. */
function draftOf(c: DigestConfig, lang: 'es' | 'en'): DigestDraft {
  const fresh = !c.last_period_end;
  return {
    enabled: c.enabled,
    frequency: c.frequency,
    weekday: c.weekday,
    hour: c.hour,
    timezone: fresh && c.timezone === 'UTC' ? browserZone() : c.timezone,
    language: fresh ? lang : c.language,
    sendEmpty: c.send_empty,
    destinations: c.destinations.map((a) => ({
      ...emptyAction(a.type),
      id: a.id,
      url: a.url ?? '',
      to: (a.to ?? []).join(', '),
      secret: a.secret ?? '',
    })),
  };
}

function configOf(d: DigestDraft): DigestConfig {
  return {
    enabled: d.enabled,
    frequency: d.frequency,
    weekday: d.weekday,
    hour: d.hour,
    timezone: d.timezone.trim(),
    language: d.language,
    send_empty: d.sendEmpty,
    destinations: d.destinations.map((a) => {
      const out: RuleAction = { id: a.id, type: a.type };
      for (const { field } of ACTION_FIELDS[a.type]) {
        if (field === 'to') {
          out.to = list(a.to);
        } else if (field === 'url' || field === 'secret') {
          out[field] = a[field].trim();
        }
      }
      return out;
    }),
  };
}

/** La sección Resumen: cuándo y a dónde va el resumen del proyecto, con su vista previa. */
@Component({
  selector: 'app-digest-settings',
  imports: [Icon, Pick],
  changeDetection: ChangeDetectionStrategy.OnPush,
  templateUrl: './digest-settings.html',
  styleUrls: ['./settings.css', './rule-editor.css', './digest-settings.css'],
})
export class DigestSettings {
  readonly projectId = input.required<number>();

  private readonly api = inject(Api);
  private readonly sanitizer = inject(DomSanitizer);
  protected readonly i18n = inject(I18n);
  protected readonly t = this.i18n.t;
  protected readonly types = TYPES;
  protected readonly icons = ACTION_ICON;
  protected readonly fields = ACTION_FIELDS;
  protected readonly zones = zones();

  protected readonly saved = signal<DigestConfig | null>(null);
  protected readonly draft = signal<DigestDraft | null>(null);
  private readonly initial = signal('');
  protected readonly dirty = computed(() => JSON.stringify(this.draft()) !== this.initial());
  protected readonly busy = signal(false);
  private readonly failure = signal<unknown>(null);
  protected readonly error = computed(() =>
    this.failure() === null ? '' : apiError(this.failure()),
  );
  private readonly saveFailure = signal<unknown>(null);
  protected readonly saveError = computed(() =>
    this.saveFailure() === null ? '' : apiError(this.saveFailure()),
  );
  protected readonly toast = signal('');
  private toastTimer?: ReturnType<typeof setTimeout>;
  protected readonly addOpen = signal(false);
  /** Los secretos de webhook recién generados, que solo se ven al guardar. */
  protected readonly secrets = signal<string[]>([]);

  protected readonly sending = signal(false);
  protected readonly results = signal<DigestResult[] | null>(null);
  private readonly sendFailure = signal<unknown>(null);
  protected readonly sendError = computed(() =>
    this.sendFailure() === null ? '' : apiError(this.sendFailure()),
  );
  protected readonly canSend = computed(
    () => !this.dirty() && (this.saved()?.destinations.length ?? 0) > 0,
  );

  protected readonly preview = signal<DigestPreview | null>(null);
  protected readonly previewFailed = signal(false);
  protected readonly previewHtml = computed<SafeHtml | null>(() => {
    const p = this.preview();
    // El HTML lo arma el servidor con todo escapado; el iframe no ejecuta scripts.
    return p ? this.sanitizer.bypassSecurityTrustHtml(p.html) : null;
  });
  private previewSub?: Subscription;

  protected readonly frequencyOptions = computed<PickOption[]>(() => {
    this.i18n.lang();
    return [
      { value: 'daily', label: this.t('digest.daily') },
      { value: 'weekly', label: this.t('digest.weekly') },
    ];
  });
  protected readonly weekdayOptions = computed<PickOption[]>(() => {
    const f = new Intl.DateTimeFormat(locale(this.i18n.lang()), {
      weekday: 'long',
      timeZone: 'UTC',
    });
    // El 23 de noviembre de 2026 es lunes.
    return WEEKDAYS.map((n) => ({
      value: String(n),
      label: f.format(Date.UTC(2026, 10, 22 + (n === 0 ? 7 : n))),
    }));
  });
  protected readonly hourOptions: PickOption[] = Array.from({ length: 24 }, (_, h) => ({
    value: String(h),
    label: `${String(h).padStart(2, '0')}:00`,
  }));
  protected readonly languageOptions: PickOption[] = LANGS.map((l) => ({
    value: l.value,
    label: l.label,
  }));

  /** «El próximo sale el lun, 23 nov, 9:00.» o lo último que salió, en la zona del resumen. */
  protected readonly status = computed(() => {
    this.i18n.lang();
    const c = this.saved();
    if (!c) {
      return '';
    }
    const when = (iso: string) =>
      new Intl.DateTimeFormat(locale(), {
        weekday: 'short',
        day: 'numeric',
        month: 'short',
        hour: '2-digit',
        minute: '2-digit',
        timeZone: c.timezone,
        timeZoneName: 'short',
      }).format(new Date(iso));
    const parts = [
      c.enabled && c.next_send_at
        ? this.t('digest.next', { date: when(c.next_send_at) })
        : this.t('digest.off'),
    ];
    if (c.last_sent_at) {
      parts.push(this.t('digest.last', { date: when(c.last_sent_at) }));
    }
    return parts.join(' ');
  });

  constructor() {
    effect(() => {
      const id = this.projectId();
      untracked(() => this.load(id));
    });
    // La vista previa sigue al idioma y la frecuencia del borrador.
    effect(() => {
      const d = this.draft();
      const id = this.projectId();
      if (d) {
        const { language, frequency } = d;
        untracked(() => this.loadPreview(id, language, frequency));
      }
    });
    inject(DestroyRef).onDestroy(() => {
      clearTimeout(this.toastTimer);
      this.previewSub?.unsubscribe();
    });
  }

  protected load(id = this.projectId()): void {
    this.failure.set(null);
    this.draft.set(null);
    this.results.set(null);
    this.secrets.set([]);
    this.api.digest(id).subscribe({
      next: (c) => this.reset(c),
      error: (err) => this.failure.set(err),
    });
  }

  private lastPreview = '';

  private loadPreview(
    id: number,
    language: string,
    frequency: DigestFrequency,
    force = false,
  ): void {
    const key = `${id}|${language}|${frequency}`;
    if (key === this.lastPreview && !force) {
      return;
    }
    this.lastPreview = key;
    this.previewSub?.unsubscribe();
    this.previewFailed.set(false);
    this.preview.set(null);
    this.previewSub = this.api.digestPreview(id, language, frequency).subscribe({
      next: (p) => this.preview.set(p),
      error: () => this.previewFailed.set(true),
    });
  }

  protected reloadPreview(): void {
    const d = this.draft();
    if (d) {
      this.loadPreview(this.projectId(), d.language, d.frequency, true);
    }
  }

  private reset(c: DigestConfig): void {
    this.saved.set(c);
    const d = draftOf(c, this.i18n.lang());
    this.draft.set(d);
    this.initial.set(JSON.stringify(d));
  }

  protected patch(p: Partial<DigestDraft>): void {
    this.draft.update((d) => (d ? { ...d, ...p } : d));
  }

  protected setDestination(i: number, field: ActionField, value: string): void {
    this.draft.update((d) =>
      d
        ? {
            ...d,
            destinations: d.destinations.map((a, j) =>
              j === i ? withField(a, field, value, this.savedDestination(a.id)) : a,
            ),
          }
        : d,
    );
  }

  protected addDestination(type: ActionType): void {
    this.addOpen.set(false);
    this.draft.update((d) =>
      d ? { ...d, destinations: [...d.destinations, emptyAction(type)] } : d,
    );
  }

  protected removeDestination(i: number): void {
    this.draft.update((d) =>
      d ? { ...d, destinations: d.destinations.filter((_, j) => j !== i) } : d,
    );
  }

  protected label(type: ActionType): string {
    return this.t(`action.${type}` as Key);
  }

  protected hint(type: ActionType): Key | undefined {
    return type === 'webhook' ? 'digest.webhookHint' : ACTION_HINT[type];
  }

  /** El iframe, a la altura del correo: sin scripts, solo se lee su altura. */
  protected fit(e: Event): void {
    const frame = e.target as HTMLIFrameElement;
    const h = frame.contentDocument?.documentElement.scrollHeight;
    if (h) {
      frame.height = String(h);
    }
  }

  private savedDestination(id: string): RuleAction | undefined {
    return this.saved()?.destinations.find((a) => a.id === id);
  }

  protected secretAgain(a: ActionDraft, field: ActionField): Key | null {
    return secretAgain(a, field, this.savedDestination(a.id));
  }

  protected isMasked(a: ActionDraft, field: ActionField): boolean {
    return a[field].includes('…');
  }

  protected result(id: string): DigestResult | undefined {
    return this.results()?.find((r) => r.id === id);
  }

  protected save(): void {
    const d = this.draft();
    if (!d || this.busy()) {
      return;
    }
    this.busy.set(true);
    this.saveFailure.set(null);
    this.api.setDigest(this.projectId(), configOf(d)).subscribe({
      next: (c) => {
        this.busy.set(false);
        const fresh = c.destinations
          .filter((a) => a.type === 'webhook' && a.secret && !a.secret.includes('…'))
          .map((a) => a.secret as string);
        this.secrets.set(fresh);
        this.reset({
          ...c,
          destinations: c.destinations.map((a) =>
            a.secret && !a.secret.includes('…') ? { ...a, secret: `…${a.secret.slice(-4)}` } : a,
          ),
        });
        this.results.set(null);
        clearTimeout(this.toastTimer);
        this.toast.set(this.t('digest.saved'));
        this.toastTimer = setTimeout(() => this.toast.set(''), 2500);
        this.reloadPreview();
      },
      error: (err) => {
        this.busy.set(false);
        this.saveFailure.set(err);
      },
    });
  }

  protected sendNow(): void {
    if (this.sending() || !this.canSend()) {
      return;
    }
    this.sending.set(true);
    this.sendFailure.set(null);
    this.results.set(null);
    this.api.sendDigest(this.projectId()).subscribe({
      next: (page) => {
        this.sending.set(false);
        this.results.set(page.data);
      },
      error: (err) => {
        this.sending.set(false);
        this.sendFailure.set(err);
      },
    });
  }
}
