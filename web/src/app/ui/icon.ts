import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';
import { ICONS, type IconWeight } from './icons';

/**
 * Un icono de Phosphor sobre el propio `<svg>`, como en el panel:
 * `<svg appIcon="copy"></svg>`. Mide 16 px salvo que el CSS diga otra cosa y toma
 * el color del texto. En duotone el primer trazo es el fondo, al 20 %.
 */
@Component({
  selector: 'svg[appIcon]',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `@for (path of paths(); track $index) {<svg:path [attr.d]="path.d" [attr.opacity]="path.opacity" />}`,
  host: {
    viewBox: '0 0 256 256',
    width: '16',
    height: '16',
    'aria-hidden': 'true',
    focusable: 'false',
    style: 'fill: currentColor; stroke: none',
  },
})
export class Icon {
  readonly appIcon = input.required<string>();
  readonly weight = input<IconWeight>('regular');

  protected readonly paths = computed(() => {
    const weight = this.weight();
    const own = ICONS[weight][this.appIcon()];
    const paths = own ?? ICONS.regular[this.appIcon()] ?? [];
    const duotone = own !== undefined && weight === 'duotone';
    return paths.map((d, i) => ({ d, opacity: duotone && i === 0 ? 0.2 : null }));
  });
}
