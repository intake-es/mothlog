import { TestBed } from '@angular/core/testing';
import { Markdown, parseMarkdown, renderMarkdown, safeHref } from './markdown';

function html(md: string): string {
  const div = document.createElement('div');
  renderMarkdown(document, div, parseMarkdown(md));
  return div.innerHTML;
}

const A = (href: string, text: string) =>
  `<a href="${href}" target="_blank" rel="noopener noreferrer">${text}</a>`;

describe('Markdown', () => {
  describe('formatos', () => {
    it('párrafos y saltos de línea', () => {
      expect(html('uno\ndos\n\ntres')).toBe('<p>uno<br>dos</p><p>tres</p>');
    });

    it('negrita y cursiva, con * y con _', () => {
      expect(html('**negrita** y __también__')).toBe(
        '<p><strong>negrita</strong> y <strong>también</strong></p>',
      );
      expect(html('*cursiva* y _también_')).toBe('<p><em>cursiva</em> y <em>también</em></p>');
      expect(html('**negrita con *cursiva* dentro**')).toBe(
        '<p><strong>negrita con <em>cursiva</em> dentro</strong></p>',
      );
      expect(html('*cursiva con **negrita** dentro*')).toBe(
        '<p><em>cursiva con <strong>negrita</strong> dentro</em></p>',
      );
    });

    it('no confunde snake_case, 2 * 3 ni asteriscos sueltos', () => {
      expect(html('user_id y snake_case_name')).toBe('<p>user_id y snake_case_name</p>');
      expect(html('2 * 3 * 4')).toBe('<p>2 * 3 * 4</p>');
      expect(html('**sin cerrar')).toBe('<p>**sin cerrar</p>');
      expect(html('\\*literal\\*')).toBe('<p>*literal*</p>');
    });

    it('código en línea, sin formato dentro', () => {
      expect(html('mira `a **b** c` y ``con ` dentro``')).toBe(
        '<p>mira <code>a **b** c</code> y <code>con ` dentro</code></p>',
      );
      expect(html('`sin cerrar')).toBe('<p>`sin cerrar</p>');
    });

    it('bloques de código con ``` y ~~~, tal cual', () => {
      expect(html('antes\n```ts\nconst a = 1;\n  **no** <b>\n```\ndespués')).toBe(
        '<p>antes</p><pre><code>const a = 1;\n  **no** &lt;b&gt;</code></pre><p>después</p>',
      );
      expect(html('~~~\nx\n~~~')).toBe('<pre><code>x</code></pre>');
      expect(html('```\nsin cerrar\nhasta el final')).toBe(
        '<pre><code>sin cerrar\nhasta el final</code></pre>',
      );
    });

    it('enlaces y direcciones sueltas', () => {
      expect(html('[la doc](https://example.com/a?b=1&c=2)')).toBe(
        `<p>${A('https://example.com/a?b=1&amp;c=2', 'la doc')}</p>`,
      );
      expect(html('[**fuerte**](http://example.com)')).toBe(
        `<p>${A('http://example.com/', '<strong>fuerte</strong>')}</p>`,
      );
      expect(html('[escribe](mailto:ana@example.com)')).toBe(
        `<p>${A('mailto:ana@example.com', 'escribe')}</p>`,
      );
      expect(html('ver https://example.com/x_(y). Y más')).toBe(
        `<p>ver ${A('https://example.com/x_(y)', 'https://example.com/x_(y)')}. Y más</p>`,
      );
      expect(html('(en https://example.com/a)')).toBe(
        `<p>(en ${A('https://example.com/a', 'https://example.com/a')})</p>`,
      );
    });

    it('listas con viñetas, numeradas y anidadas', () => {
      expect(html('- uno\n- **dos**\n* otra lista')).toBe(
        '<ul><li>uno</li><li><strong>dos</strong></li></ul><ul><li>otra lista</li></ul>',
      );
      expect(html('3. tres\n4. cuatro')).toBe('<ol start="3"><li>tres</li><li>cuatro</li></ol>');
      expect(html('1. uno\n   - a\n   - b\n2. dos')).toBe(
        '<ol><li><p>uno</p><ul><li>a</li><li>b</li></ul></li><li>dos</li></ol>',
      );
      expect(html('Pasos:\n- uno\n- dos\n\nFin')).toBe(
        '<p>Pasos:</p><ul><li>uno</li><li>dos</li></ul><p>Fin</p>',
      );
    });

    it('títulos', () => {
      expect(html('## Causa\nUn nulo')).toBe('<p class="md-h md-h2">Causa</p><p>Un nulo</p>');
    });
  });

  describe('saneado', () => {
    it('el HTML del comentario sale como texto', () => {
      const div = document.createElement('div');
      renderMarkdown(
        document,
        div,
        parseMarkdown(
          '<img src=x onerror="alert(1)">\n<script>alert(1)</script>\n**<b>hola</b>**\n- <iframe src="https://example.com"></iframe>',
        ),
      );
      expect(div.querySelector('img, script, b, iframe')).toBeNull();
      expect(div.textContent).toContain('<img src=x onerror="alert(1)">');
      expect(div.textContent).toContain('<script>alert(1)</script>');
      expect(div.querySelector('strong')?.textContent).toBe('<b>hola</b>');
      expect(div.querySelector('li')?.textContent).toBe(
        '<iframe src="https://example.com"></iframe>',
      );
      // Solo los enlaces llevan atributos, los suyos: la dirección suelta del iframe es un enlace.
      const attrs = Array.from(div.querySelectorAll('*')).flatMap((e) =>
        Array.from(e.attributes).map((a) => `${e.tagName.toLowerCase()} ${a.name}`),
      );
      expect(attrs).toEqual(['a href', 'a target', 'a rel']);
      expect(div.querySelector('a')?.getAttribute('href')).toBe('https://example.com/');
    });

    it('los enlaces que no son http, https ni mailto se quedan en texto', () => {
      for (const url of [
        'javascript:alert(1)',
        'JaVaScRiPt:alert(1)',
        ' javascript:alert(1)',
        'java\tscript:alert(1)',
        'data:text/html,<script>alert(1)</script>',
        'vbscript:msgbox(1)',
        'file:///etc/passwd',
        '//example.com',
        '/relativa',
        '#ancla',
      ]) {
        const out = html(`[pulsa](${url})`);
        expect(out, url).not.toContain('<a');
        expect(out, url).toContain('pulsa');
      }
      expect(html('javascript:alert(1)')).toBe('<p>javascript:alert(1)</p>');
    });

    it('no se pueden colar atributos por la URL ni por el texto', () => {
      const out = html('[x](https://example.com/"onmouseover="alert(1))');
      const div = document.createElement('div');
      div.innerHTML = out;
      const a = div.querySelector('a');
      expect(a?.getAttributeNames().sort()).toEqual(['href', 'rel', 'target']);
      expect(a?.getAttribute('href')).toBe('https://example.com/%22onmouseover=%22alert(1)');
      expect(html('[" onclick="alert(1)](https://example.com)')).toBe(
        `<p>${A('https://example.com/', '" onclick="alert(1)')}</p>`,
      );
    });

    it('safeHref', () => {
      expect(safeHref('https://example.com')).toBe('https://example.com/');
      expect(safeHref('mailto:a@b.c')).toBe('mailto:a@b.c');
      expect(safeHref('javascript:alert(1)')).toBeNull();
      expect(safeHref('https://')).toBeNull();
    });
  });

  it('el componente vuelve a pintar al cambiar el texto, sin innerHTML', async () => {
    const fixture = TestBed.createComponent(Markdown);
    fixture.componentRef.setInput('text', '**uno** <i>x</i>');
    fixture.detectChanges();
    await fixture.whenStable();
    const el: HTMLElement = fixture.nativeElement;
    expect(el.classList.contains('md')).toBe(true);
    expect(el.innerHTML).toBe('<p><strong>uno</strong> &lt;i&gt;x&lt;/i&gt;</p>');
    fixture.componentRef.setInput('text', '- dos');
    fixture.detectChanges();
    await fixture.whenStable();
    expect(el.innerHTML).toBe('<ul><li>dos</li></ul>');
  });
});
