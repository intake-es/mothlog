import {
  ChangeDetectionStrategy,
  Component,
  computed,
  DestroyRef,
  type ElementRef,
  effect,
  inject,
  input,
  output,
  signal,
  untracked,
  viewChild,
} from '@angular/core';
import type { Subscription } from 'rxjs';
import {
  Api,
  type Assignee,
  type QueryError,
  type TagKey,
  type TagValueCount,
} from '../../core/api';
import { I18n, type Key, plural } from '../../core/i18n';
import {
  BUILTIN_KEYS,
  MINE,
  replaceToken,
  type SearchStatus,
  STATUSES,
  type Token,
  term,
  tokenAt,
} from '../../core/search';
import { Icon } from '../../ui/icon';

interface Suggestion {
  /** Lo que se escribe en la búsqueda en lugar del término bajo el cursor. */
  text: string;
  label: string;
  detail: string;
  /** Si cierra un valor, se busca al elegirla. */
  submit: boolean;
}

interface Section {
  title: string;
  items: Suggestion[];
  hint?: string;
}

const STATUS_LABEL: Record<SearchStatus, Key> = {
  open: 'filter.open',
  resolved: 'filter.resolved',
  resolved_next: 'filter.resolved_next',
  silenced: 'filter.silenced',
  regressed: 'filter.regressed',
  assigned: 'search.is.assigned',
  unassigned: 'search.is.unassigned',
};

const KEY_LABEL: Record<(typeof BUILTIN_KEYS)[number], Key> = {
  is: 'search.key.is',
  assigned: 'search.key.assigned',
  release: 'search.key.release',
  environment: 'search.key.environment',
  level: 'search.key.level',
  project: 'search.key.project',
  user: 'search.key.user',
};

const EXAMPLES: { query: string; label: Key }[] = [
  { query: 'is:regressed', label: 'search.ex.regressed' },
  { query: 'assigned:me', label: 'search.ex.mine' },
  { query: 'is:unassigned', label: 'search.ex.unassigned' },
  { query: 'browser:Safari', label: 'search.ex.browser' },
  { query: 'os:"Mac OS"', label: 'search.ex.os' },
  { query: '!environment:staging', label: 'search.ex.notEnv' },
  { query: 'user:ana@example.com', label: 'search.ex.user' },
  { query: '"division by zero"', label: 'search.ex.text' },
];

const MAX_KEYS = 8;

/** La barra de búsqueda de la lista, con autocompletado de claves y valores. */
@Component({
  selector: 'app-search-bar',
  imports: [Icon],
  changeDetection: ChangeDetectionStrategy.OnPush,
  templateUrl: './search-bar.html',
  styleUrl: './search-bar.css',
})
export class SearchBar {
  /** La búsqueda aplicada, la de la URL. */
  readonly query = input.required<string>();
  readonly error = input<QueryError | null>(null);
  readonly projects = input<string[]>([]);
  readonly submitted = output<string>();

  private readonly api = inject(Api);
  protected readonly t = inject(I18n).t;

  protected readonly text = signal('');
  private readonly caret = signal(0);
  protected readonly focused = signal(false);
  protected readonly open = signal(false);
  protected readonly active = signal(-1);
  private readonly keys = signal<TagKey[]>([]);
  private keysAsked = false;
  private readonly values = signal<{ key: string; prefix: string; data: TagValueCount[] } | null>(
    null,
  );
  private readonly assignees = signal<Assignee[]>([]);
  private assigneesAsked = false;
  private valuesSub?: Subscription;
  private valuesTimer?: ReturnType<typeof setTimeout>;
  private readonly input = viewChild.required<ElementRef<HTMLInputElement>>('input');

  protected readonly token = computed<Token>(() => tokenAt(this.text(), this.caret()));

  private readonly candidates = computed<Section[]>(() => {
    const tok = this.token();
    const neg = tok.negate;
    if (tok.key === null) {
      const prefix = tok.value.toLowerCase();
      const builtin = BUILTIN_KEYS.map((k) => ({ key: k, detail: this.t(KEY_LABEL[k]) }));
      const tags = this.keys()
        .filter((k) => !(BUILTIN_KEYS as readonly string[]).includes(k.key))
        .map((k) => ({ key: k.key, detail: plural('search.inIssues', k.issues) }));
      const items = [...builtin, ...tags]
        .filter((k) => k.key.toLowerCase().startsWith(prefix))
        .slice(0, MAX_KEYS)
        .map((k) => ({
          text: `${neg ? '!' : ''}${k.key}:`,
          label: `${k.key}:`,
          detail: k.detail,
          submit: false,
        }));
      const out: Section[] = [];
      if (items.length) {
        out.push({ title: this.t('search.keys'), items });
      }
      if (tok.raw === '') {
        out.push({
          title: this.t('search.examples'),
          items: EXAMPLES.map((e) => ({
            text: e.query,
            label: e.query,
            detail: this.t(e.label),
            submit: true,
          })),
        });
      }
      return out;
    }

    const key = tok.key;
    const prefix = tok.value.toLowerCase();
    const title = this.t('search.values', { key });
    const value = (v: string, detail: string): Suggestion => ({
      text: term(key, v, neg),
      label: v,
      detail,
      submit: true,
    });
    if (key === 'is') {
      const items = STATUSES.filter((s) => s.startsWith(prefix)).map((s) =>
        value(s, this.t(STATUS_LABEL[s])),
      );
      return [{ title, items }];
    }
    if (key === 'project') {
      const items = this.projects()
        .filter((p) => p.toLowerCase().startsWith(prefix))
        .map((p) => value(p, ''));
      return [{ title, items }];
    }
    if (key === 'assigned') {
      const items = [
        value(MINE, this.t('search.assigned.me')),
        ...this.assignees().map((a) =>
          value(
            a.name,
            a.kind === 'token'
              ? this.t('search.assigned.agent')
              : a.display_name || this.t('search.assigned.person'),
          ),
        ),
      ].filter((s) => s.label.toLowerCase().startsWith(prefix));
      return [{ title, items }];
    }
    if (key === 'user') {
      return [{ title, items: [], hint: this.t('search.userHint') }];
    }
    const loaded = this.values();
    if (!loaded || loaded.key !== key || loaded.prefix !== tok.value) {
      return [];
    }
    const items = loaded.data.map((v) => value(v.value, plural('events', v.count)));
    return [{ title, items, hint: items.length ? undefined : this.t('search.noValues', { key }) }];
  });

  protected readonly sections = computed(() =>
    this.candidates().filter((s) => s.items.length > 0 || !!s.hint),
  );

  protected readonly flat = computed(() => this.sections().flatMap((s) => s.items));

  protected readonly pending = computed(
    () => this.focused() && this.text().trim() !== this.query().trim(),
  );

  protected readonly errorText = computed(() => {
    const e = this.error();
    if (!e) {
      return '';
    }
    const key = `search.error.${e.code}` as Key;
    const message = this.t(key, { key: e.key ?? '', value: e.value ?? '' });
    return this.t('search.error.at', { n: e.position + 1, message });
  });

  constructor() {
    effect(() => {
      const q = this.query();
      untracked(() => {
        this.text.set(q);
        this.caret.set(q.length);
      });
    });
    effect(() => {
      const tok = this.token();
      const focused = this.focused();
      untracked(() => this.askValues(tok, focused));
    });
    inject(DestroyRef).onDestroy(() => {
      this.valuesSub?.unsubscribe();
      clearTimeout(this.valuesTimer);
    });
  }

  private askValues(tok: Token, focused: boolean): void {
    const key = tok.key;
    if (focused && key === 'assigned' && !this.assigneesAsked) {
      this.assigneesAsked = true;
      this.api.assignees().subscribe({
        next: (page) => this.assignees.set(page.data),
        error: () => this.assignees.set([]),
      });
    }
    if (!focused || key === null || (BUILTIN_KEYS as readonly string[]).includes(key)) {
      return;
    }
    const v = this.values();
    if (v && v.key === key && v.prefix === tok.value) {
      return;
    }
    clearTimeout(this.valuesTimer);
    this.valuesTimer = setTimeout(() => {
      this.valuesSub?.unsubscribe();
      this.valuesSub = this.api.tagValues(key, tok.value).subscribe({
        next: (page) => this.values.set({ key, prefix: tok.value, data: page.data }),
        error: () => this.values.set({ key, prefix: tok.value, data: [] }),
      });
    }, 120);
  }

  protected onFocus(): void {
    this.focused.set(true);
    this.open.set(true);
    this.syncCaret();
    if (!this.keysAsked) {
      this.keysAsked = true;
      this.api.tagKeys().subscribe({
        next: (page) => this.keys.set(page.data),
        error: () => this.keys.set([]),
      });
    }
  }

  protected onBlur(): void {
    this.focused.set(false);
    this.open.set(false);
  }

  protected onInput(value: string): void {
    this.text.set(value);
    this.syncCaret();
    this.open.set(true);
    this.active.set(-1);
  }

  protected syncCaret(): void {
    const el = this.input().nativeElement;
    this.caret.set(el.selectionStart ?? el.value.length);
  }

  protected onKeydown(e: KeyboardEvent): void {
    const n = this.flat().length;
    switch (e.key) {
      case 'ArrowDown':
      case 'ArrowUp':
        if (!n) {
          return;
        }
        e.preventDefault();
        this.open.set(true);
        this.active.update((i) => (e.key === 'ArrowDown' ? (i + 1) % n : i <= 0 ? n - 1 : i - 1));
        return;
      case 'Tab':
        if (this.open() && n && !e.shiftKey) {
          e.preventDefault();
          this.choose(this.flat()[Math.max(this.active(), 0)]);
        }
        return;
      case 'Enter': {
        e.preventDefault();
        const s = this.open() ? this.flat()[this.active()] : undefined;
        if (s) {
          this.choose(s);
        } else {
          this.submit();
        }
        return;
      }
      case 'Escape':
        if (this.open()) {
          e.stopPropagation();
          this.open.set(false);
        }
        return;
    }
  }

  protected choose(s: Suggestion): void {
    const { query, caret } = replaceToken(this.text(), this.token(), s.text);
    this.text.set(query);
    this.caret.set(caret);
    this.active.set(-1);
    const el = this.input().nativeElement;
    el.value = query;
    el.focus();
    el.setSelectionRange(caret, caret);
    if (s.submit) {
      this.submit();
    } else {
      this.open.set(true);
    }
  }

  protected submit(): void {
    this.open.set(false);
    this.submitted.emit(this.text().trim());
  }

  protected clear(): void {
    this.text.set('');
    this.caret.set(0);
    const el = this.input().nativeElement;
    el.value = '';
    el.focus();
    this.open.set(true);
  }

  protected index(s: Suggestion): number {
    return this.flat().indexOf(s);
  }
}
