/**
 * Copia el texto que dará text. Safari solo deja escribir dentro del gesto del
 * usuario: con ClipboardItem la escritura empieza en el clic y espera a la
 * promesa. Sin él, se espera el texto y se escribe; sin la API del
 * portapapeles (http fuera de localhost), con un textarea.
 */
export async function copyText(text: Promise<string>): Promise<void> {
  if (typeof ClipboardItem !== 'undefined' && navigator.clipboard?.write) {
    const blob = text.then((t) => new Blob([t], { type: 'text/plain' }));
    try {
      await navigator.clipboard.write([new ClipboardItem({ 'text/plain': blob })]);
      return;
    } catch (err) {
      // Si falló al descargar el texto, no hay nada que copiar por otro camino.
      await text;
      if (!navigator.clipboard?.writeText) {
        throw err;
      }
    }
  }
  const value = await text;
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(value);
      return;
    } catch {
      // Sin permiso: el camino de abajo.
    }
  }
  const area = document.createElement('textarea');
  area.value = value;
  area.setAttribute('readonly', '');
  area.style.position = 'fixed';
  area.style.opacity = '0';
  document.body.appendChild(area);
  area.select();
  const ok = document.execCommand('copy');
  area.remove();
  if (!ok) {
    throw new Error('no se pudo copiar');
  }
}
