import { NgTemplateOutlet } from '@angular/common';
import { ChangeDetectionStrategy, Component, computed, inject, input, signal } from '@angular/core';
import type { ExceptionValue, Frame } from '../../core/api';
import { I18n, t } from '../../core/i18n';
import { Icon } from '../../ui/icon';

export interface CodeLine {
  n: number | null;
  text: string;
  error: boolean;
}

export interface FrameView {
  key: string;
  frame: Frame;
  file: string;
  fn: string;
  position: string;
  inApp: boolean;
  lines: CodeLine[];
}

export type FrameGroup =
  | { kind: 'app'; key: string; frame: FrameView }
  | { kind: 'libs'; key: string; frames: FrameView[] };

export interface ExceptionView {
  key: string;
  type: string;
  value: string;
  handled: boolean | undefined;
  mechanism: string;
  groups: FrameGroup[];
  empty: boolean;
}

/** Las líneas de código del fotograma, numeradas desde el contexto anterior. */
export function codeLines(f: Frame): CodeLine[] {
  if (f.context_line === undefined || f.context_line === null) {
    return [];
  }
  const pre = f.pre_context ?? [];
  const post = f.post_context ?? [];
  const line = f.lineno ?? null;
  const at = (offset: number) => (line === null ? null : line + offset);
  return [
    ...pre.map((text, i) => ({ n: at(i - pre.length), text, error: false })),
    { n: line, text: f.context_line, error: true },
    ...post.map((text, i) => ({ n: at(i + 1), text, error: false })),
  ];
}

function frameView(f: Frame, key: string, inApp: boolean): FrameView {
  const file = f.filename || f.abs_path || f.module || t('stack.unknownFile');
  const pos = [f.lineno, f.colno].filter((n) => n !== undefined && n !== null).join(':');
  return {
    key,
    frame: f,
    file,
    fn: f.function || '',
    position: pos,
    inApp,
    lines: codeLines(f),
  };
}

/**
 * Propio es lo que el SDK marca in_app y tiene código o no es un .js servido:
 * el SDK del navegador marca in_app también los chunks sin mapa, zone.js y
 * `<anonymous>`, que se leen como librerías.
 */
export function isOwn(f: Frame): boolean {
  if (f.in_app !== true) {
    return false;
  }
  const file = f.filename || f.abs_path || '';
  const hasCode = f.context_line !== undefined && f.context_line !== null;
  return hasCode || !/^(https?:\/\/|<anonymous>|\[native code\])/.test(file);
}

/**
 * Agrupa los fotogramas, del que falló al más externo: cada uno propio va suelto
 * y los de librerías seguidos se pliegan juntos. Si no hay ninguno propio, todos
 * cuentan como propios para no esconder la traza entera.
 */
export function groupFrames(frames: Frame[], prefix = ''): FrameGroup[] {
  const ordered = [...frames].reverse();
  const anyApp = ordered.some(isOwn);
  const groups: FrameGroup[] = [];
  ordered.forEach((f, i) => {
    const inApp = anyApp ? isOwn(f) : true;
    const view = frameView(f, `${prefix}${i}`, inApp);
    const last = groups[groups.length - 1];
    if (inApp) {
      groups.push({ kind: 'app', key: view.key, frame: view });
    } else if (last?.kind === 'libs') {
      last.frames.push(view);
    } else {
      groups.push({ kind: 'libs', key: `libs-${view.key}`, frames: [view] });
    }
  });
  return groups;
}

/** La cadena en el orden en que se lee: la que se lanzó primero y luego sus causas. */
export function exceptionViews(excs: ExceptionValue[]): ExceptionView[] {
  return [...excs].reverse().map((x, i) => {
    const groups = groupFrames(x.frames ?? [], `${i}-`);
    return {
      key: String(i),
      type: x.type || '',
      value: x.value || '',
      handled: x.mechanism?.handled,
      mechanism: x.mechanism?.type || '',
      groups,
      empty: groups.length === 0,
    };
  });
}

@Component({
  selector: 'app-stacktrace',
  imports: [Icon, NgTemplateOutlet],
  changeDetection: ChangeDetectionStrategy.OnPush,
  templateUrl: './stacktrace.html',
  styleUrl: './stacktrace.css',
})
export class Stacktrace {
  readonly exceptions = input.required<ExceptionValue[]>();

  protected readonly t = inject(I18n).t;
  protected readonly plural = inject(I18n).plural;

  protected readonly views = computed(() => exceptionViews(this.exceptions()));

  /** Lo que el usuario ha abierto o cerrado a mano; el resto, según sea propio o no. */
  private readonly toggled = signal<ReadonlySet<string>>(new Set());

  protected open(key: string, byDefault: boolean): boolean {
    return this.toggled().has(key) ? !byDefault : byDefault;
  }

  protected toggle(key: string): void {
    this.toggled.update((s) => {
      const next = new Set(s);
      if (!next.delete(key)) {
        next.add(key);
      }
      return next;
    });
  }
}
