import { ChangeDetectionStrategy, Component } from '@angular/core';

/** La polilla de Mothlog sobre el propio `<svg>`: `<svg appLogo></svg>`. Toma el color del texto. */
@Component({
  selector: 'svg[appLogo]',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template:
    '<svg:path d="M30.2 19.6C22.6 25.2 14.4 36.4 9 50.8C14.2 54.4 22.4 55.8 30.2 54ZM33.8 19.6C41.4 25.2 49.6 36.4 55 50.8C49.8 54.4 41.6 55.8 33.8 54ZM30.8 12.8C28.4 9 22.8 5.2 15.4 4.6C19 7.8 24.8 11.2 30.8 12.8ZM33.2 12.8C35.6 9 41.2 5.2 48.6 4.6C45 7.8 39.2 11.2 33.2 12.8ZM27.8 15.6a4.2 4.2 0 1 0 8.4 0a4.2 4.2 0 1 0-8.4 0Z" />',
  host: {
    viewBox: '4 2.2 56 56',
    width: '16',
    height: '16',
    'aria-hidden': 'true',
    focusable: 'false',
    style: 'fill: currentColor; stroke: none',
  },
})
export class Logo {}
