import {
  ChangeDetectionStrategy,
  Component,
  DOCUMENT,
  ElementRef,
  effect,
  inject,
  input,
  ViewEncapsulation,
} from '@angular/core';

/**
 * Un subconjunto de Markdown: párrafos, títulos, listas (también anidadas),
 * bloques de código, código, negrita, cursiva y enlaces. Se pinta con nodos y
 * textContent, nunca con innerHTML: nada del texto llega al DOM como HTML.
 */
export type Inline =
  | { type: 'text'; text: string }
  | { type: 'code'; text: string }
  | { type: 'strong' | 'em'; children: Inline[] }
  | { type: 'link'; href: string; children: Inline[] }
  | { type: 'br' };

export type Block =
  | { type: 'p'; children: Inline[] }
  | { type: 'heading'; level: number; children: Inline[] }
  | { type: 'code'; lang: string; text: string }
  | { type: 'list'; ordered: boolean; start: number; items: Block[][] };

const TICK = String.fromCharCode(96);
const FENCE = /^ {0,3}(\x60{3,}|~{3,})\s*([^\x60\s]*)[^\x60]*$/;
const ITEM = /^( {0,3})([-*+]|\d{1,9}[.)])( +|$)(.*)$/;
const HEADING = /^ {0,3}(#{1,6})(?:[ \t]+(.*?))?(?:[ \t]+#+)?[ \t]*$/;

/** Solo enlaces http, https y mailto; el resto se queda en texto. */
export function safeHref(url: string): string | null {
  const trimmed = url.trim();
  if (!/^(https?:\/\/|mailto:)/i.test(trimmed)) {
    return null;
  }
  try {
    const u = new URL(trimmed);
    return ['http:', 'https:', 'mailto:'].includes(u.protocol) ? u.href : null;
  } catch {
    return null;
  }
}

export function parseMarkdown(source: string): Block[] {
  return blocks(source.replace(/\r\n?/g, '\n').split('\n'));
}

function blocks(lines: string[]): Block[] {
  const out: Block[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (!line.trim()) {
      i++;
      continue;
    }
    const fence = FENCE.exec(line);
    if (fence) {
      const mark = fence[1];
      const body: string[] = [];
      i++;
      while (i < lines.length) {
        const close = lines[i].trim();
        if (close.startsWith(mark[0].repeat(mark.length)) && /^(\x60+|~+)$/.test(close)) {
          i++;
          break;
        }
        body.push(lines[i]);
        i++;
      }
      out.push({ type: 'code', lang: fence[2], text: body.join('\n') });
      continue;
    }
    const heading = HEADING.exec(line);
    if (heading) {
      out.push({
        type: 'heading',
        level: heading[1].length,
        children: inline(heading[2] ?? ''),
      });
      i++;
      continue;
    }
    const item = ITEM.exec(line);
    if (item) {
      i = list(lines, i, out);
      continue;
    }
    const para: string[] = [];
    while (
      i < lines.length &&
      lines[i].trim() &&
      !(para.length && (FENCE.test(lines[i]) || HEADING.test(lines[i]) || ITEM.test(lines[i])))
    ) {
      para.push(lines[i].trim());
      i++;
    }
    out.push({ type: 'p', children: inline(para.join('\n')) });
  }
  return out;
}

/** Lee una lista desde la línea i y devuelve dónde sigue. */
function list(lines: string[], i: number, out: Block[]): number {
  const first = ITEM.exec(lines[i]) as RegExpExecArray;
  const ordered = /\d/.test(first[2]);
  const marker = ordered ? first[2].slice(-1) : first[2];
  const items: Block[][] = [];
  while (i < lines.length) {
    const m = ITEM.exec(lines[i]);
    if (!m || /\d/.test(m[2]) !== ordered || (ordered ? m[2].slice(-1) : m[2]) !== marker) {
      break;
    }
    const indent = m[1].length + m[2].length + Math.max(1, Math.min(m[3].length, 4));
    const body = [m[4]];
    i++;
    while (i < lines.length) {
      const next = lines[i];
      if (!next.trim()) {
        // Una línea en blanco sigue la lista solo si detrás viene más de ella.
        const after = lines[i + 1] ?? '';
        if (after.trim() && (leading(after) >= indent || ITEM.exec(after)?.[2] === m[2])) {
          body.push('');
          i++;
          continue;
        }
        break;
      }
      if (leading(next) >= Math.min(indent, 2)) {
        body.push(next.slice(Math.min(leading(next), indent)));
        i++;
        continue;
      }
      if (ITEM.test(next) || FENCE.test(next) || HEADING.test(next)) {
        break;
      }
      // Continuación perezosa del párrafo del elemento.
      body.push(next);
      i++;
    }
    items.push(blocks(body));
  }
  out.push({ type: 'list', ordered, start: ordered ? Number.parseInt(first[2], 10) : 1, items });
  return i;
}

function leading(s: string): number {
  return s.length - s.trimStart().length;
}

const PUNCT = /[!-/:-@[-\x60{-~]/;

export function inline(src: string): Inline[] {
  const out: Inline[] = [];
  let text = '';
  const flush = () => {
    if (text) {
      out.push({ type: 'text', text });
      text = '';
    }
  };
  let i = 0;
  while (i < src.length) {
    const c = src[i];
    if (c === '\\' && i + 1 < src.length && PUNCT.test(src[i + 1])) {
      text += src[i + 1];
      i += 2;
      continue;
    }
    if (c === '\n') {
      flush();
      out.push({ type: 'br' });
      i++;
      continue;
    }
    if (c === TICK) {
      const run = /^\x60+/.exec(src.slice(i))?.[0] ?? TICK;
      const end = src.indexOf(run, i + run.length);
      if (end > 0) {
        flush();
        let code = src.slice(i + run.length, end).replace(/\n/g, ' ');
        if (/^ .* $/.test(code) && code.trim()) {
          code = code.slice(1, -1);
        }
        out.push({ type: 'code', text: code });
        i = end + run.length;
        continue;
      }
      text += run;
      i += run.length;
      continue;
    }
    if ((c === '*' || c === '_') && src[i + 1] === c) {
      const end = closing(src, i + 2, c + c);
      if (end > 0 && opens(src, i, c, 2)) {
        flush();
        out.push({ type: 'strong', children: inline(src.slice(i + 2, end)) });
        i = end + 2;
        continue;
      }
    }
    if (c === '*' || c === '_') {
      const end = closing(src, i + 1, c);
      if (end > 0 && opens(src, i, c, 1)) {
        flush();
        out.push({ type: 'em', children: inline(src.slice(i + 1, end)) });
        i = end + 1;
        continue;
      }
    }
    if (c === '[') {
      const link = linkAt(src, i);
      if (link) {
        flush();
        const href = safeHref(link.url);
        if (href) {
          out.push({ type: 'link', href, children: inline(link.label) });
        } else {
          out.push(...inline(link.label));
        }
        i = link.end;
        continue;
      }
    }
    if (/^https?:\/\//i.test(src.slice(i, i + 8)) && !/\w/.test(src[i - 1] ?? '')) {
      let url = /^[^\s<>]+/.exec(src.slice(i))?.[0] ?? '';
      url = url.replace(/[.,;:!?\x22\x27*_]+$/, '');
      // Un paréntesis de cierre al final solo es del enlace si abre otro dentro.
      while (url.endsWith(')') && count(url, '(') < count(url, ')')) {
        url = url.slice(0, -1);
      }
      const href = safeHref(url);
      if (href) {
        flush();
        out.push({ type: 'link', href, children: [{ type: 'text', text: url }] });
        i += url.length;
        continue;
      }
    }
    text += c;
    i++;
  }
  flush();
  return out;
}

function count(s: string, ch: string): number {
  return s.split(ch).length - 1;
}

/** Abre énfasis si lo que sigue no es un espacio y, con «_», si no va pegado a una palabra. */
function opens(src: string, i: number, ch: string, len: number): boolean {
  const next = src[i + len] ?? '';
  if (!next || /\s/.test(next)) {
    return false;
  }
  return ch !== '_' || !/[\p{L}\p{N}]/u.test(src[i - 1] ?? '');
}

/** Dónde cierra el énfasis que abre delim, sin cruzar el código ni pegado a un espacio. */
function closing(src: string, from: number, delim: string): number {
  let i = from;
  while (i < src.length) {
    if (src[i] === '\\') {
      i += 2;
      continue;
    }
    if (src[i] === TICK) {
      const run = /^\x60+/.exec(src.slice(i))?.[0] ?? TICK;
      const end = src.indexOf(run, i + run.length);
      i = end > 0 ? end + run.length : i + run.length;
      continue;
    }
    if (
      src.startsWith(delim, i) &&
      i > from &&
      !/\s/.test(src[i - 1]) &&
      // «**» no cierra un «*»: es otro énfasis dentro.
      (delim.length === 2 || src[i + 1] !== delim) &&
      (delim[0] !== '_' || !/[\p{L}\p{N}]/u.test(src[i + delim.length] ?? ''))
    ) {
      return i;
    }
    if (delim.length === 1 && src.startsWith(delim + delim, i)) {
      const inner = closing(src, i + 2, delim + delim);
      if (inner > 0) {
        i = inner + 2;
        continue;
      }
    }
    i++;
  }
  return -1;
}

/** «[texto](url)» desde i, con corchetes anidados en el texto y paréntesis equilibrados en la URL. */
function linkAt(src: string, i: number): { label: string; url: string; end: number } | null {
  let depth = 0;
  let j = i;
  for (; j < src.length; j++) {
    if (src[j] === '\\') {
      j++;
    } else if (src[j] === '[') {
      depth++;
    } else if (src[j] === ']' && --depth === 0) {
      break;
    }
  }
  if (j >= src.length || src[j + 1] !== '(') {
    return null;
  }
  let k = j + 2;
  let parens = 0;
  for (; k < src.length; k++) {
    const ch = src[k];
    if (/\s/.test(ch)) {
      return null;
    }
    if (ch === '(') {
      parens++;
    } else if (ch === ')') {
      if (parens === 0) {
        break;
      }
      parens--;
    }
  }
  if (k >= src.length) {
    return null;
  }
  return { label: src.slice(i + 1, j), url: src.slice(j + 2, k), end: k + 1 };
}

/** Pinta los bloques como nodos del DOM, con el texto siempre en textContent. */
export function renderMarkdown(doc: Document, parent: Node, list: Block[]): void {
  for (const b of list) {
    parent.appendChild(renderBlock(doc, b));
  }
}

function renderBlock(doc: Document, b: Block): Node {
  switch (b.type) {
    case 'p': {
      const p = doc.createElement('p');
      renderInline(doc, p, b.children);
      return p;
    }
    case 'heading': {
      const h = doc.createElement('p');
      h.className = ['md-h', `md-h${Math.min(b.level, 3)}`].join(' ');
      renderInline(doc, h, b.children);
      return h;
    }
    case 'code': {
      const pre = doc.createElement('pre');
      const code = doc.createElement('code');
      code.textContent = b.text;
      pre.appendChild(code);
      return pre;
    }
    case 'list': {
      const el = doc.createElement(b.ordered ? 'ol' : 'ul');
      if (b.ordered && b.start !== 1) {
        el.setAttribute('start', String(b.start));
      }
      for (const item of b.items) {
        const li = doc.createElement('li');
        // Un elemento de un solo párrafo, sin el <p>: la lista queda junta.
        if (item.length === 1 && item[0].type === 'p') {
          renderInline(doc, li, item[0].children);
        } else {
          renderMarkdown(doc, li, item);
        }
        el.appendChild(li);
      }
      return el;
    }
  }
}

function renderInline(doc: Document, parent: Node, list: Inline[]): void {
  for (const n of list) {
    switch (n.type) {
      case 'text':
        parent.appendChild(doc.createTextNode(n.text));
        break;
      case 'br':
        parent.appendChild(doc.createElement('br'));
        break;
      case 'code': {
        const code = doc.createElement('code');
        code.textContent = n.text;
        parent.appendChild(code);
        break;
      }
      case 'strong':
      case 'em': {
        const el = doc.createElement(n.type);
        renderInline(doc, el, n.children);
        parent.appendChild(el);
        break;
      }
      case 'link': {
        const a = doc.createElement('a');
        a.setAttribute('href', n.href);
        a.setAttribute('target', '_blank');
        a.setAttribute('rel', 'noopener noreferrer');
        renderInline(doc, a, n.children);
        parent.appendChild(a);
        break;
      }
    }
  }
}

/** Un comentario en Markdown, saneado: ver parseMarkdown. */
@Component({
  selector: 'app-markdown',
  changeDetection: ChangeDetectionStrategy.OnPush,
  encapsulation: ViewEncapsulation.None,
  host: { class: 'md' },
  template: '',
  styles: `
    .md {
      display: block;
      color: var(--text);
      font-size: 0.8125rem;
      line-height: 1.55;
      overflow-wrap: anywhere;
    }
    .md > :first-child {
      margin-top: 0;
    }
    .md > :last-child {
      margin-bottom: 0;
    }
    .md p,
    .md ul,
    .md ol,
    .md pre {
      margin: 0 0 0.55rem;
    }
    .md .md-h {
      margin: 0.75rem 0 0.35rem;
      font-weight: 600;
    }
    .md .md-h1 {
      font-size: 0.95rem;
    }
    .md .md-h2 {
      font-size: 0.875rem;
    }
    .md ul,
    .md ol {
      padding-left: 1.25rem;
    }
    .md li + li {
      margin-top: 0.15rem;
    }
    .md li > ul,
    .md li > ol {
      margin: 0.15rem 0 0;
    }
    .md strong {
      font-weight: 600;
    }
    .md a {
      color: var(--text);
      text-decoration: underline;
      text-decoration-color: var(--text-subtle);
      text-underline-offset: 2px;
    }
    .md a:hover {
      text-decoration-color: var(--text);
    }
    .md code {
      padding: 0.05rem 0.3rem;
      border-radius: 4px;
      background: var(--bg-hover);
      font-family: var(--font-mono);
      font-size: 0.75rem;
    }
    .md pre {
      padding: 0.55rem 0.7rem;
      border: 1px solid var(--border);
      border-radius: var(--radius-sm, 6px);
      background: var(--bg-panel);
      overflow-x: auto;
    }
    .md pre code {
      padding: 0;
      background: none;
      white-space: pre;
      overflow-wrap: normal;
    }
  `,
})
export class Markdown {
  readonly text = input.required<string>();

  constructor() {
    const host = inject(ElementRef<HTMLElement>).nativeElement as HTMLElement;
    const doc = inject(DOCUMENT);
    effect(() => {
      const tree = parseMarkdown(this.text());
      host.replaceChildren();
      renderMarkdown(doc, host, tree);
    });
  }
}
