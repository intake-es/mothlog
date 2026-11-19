import * as Sentry from '@sentry/browser';

declare const SENTRY_DSN: string;
declare const SENTRY_RELEASE: string;

Sentry.init({ dsn: SENTRY_DSN, release: SENTRY_RELEASE });

const { enviarPedido } = await import('./carrito');

document.querySelector('#enviar')?.addEventListener('click', () => {
  enviarPedido('p_1042');
});
