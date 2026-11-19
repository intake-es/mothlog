import { bus, type Pedido } from './bus';

const resumen = document.querySelector<HTMLOutputElement>('#resumen');

function contarUnidades(pedido: Pedido): number {
  // Un pedido recién creado aún no tiene líneas.
  return pedido.lineas!.reduce((total, linea) => total + linea.cantidad, 0);
}

function pintarResumen(pedido: Pedido): void {
  if (resumen) {
    resumen.value = `${contarUnidades(pedido)} unidades`;
  }
}

bus.on('pedido', pintarResumen);

export function enviarPedido(id: string): void {
  bus.emit('pedido', { id });
}
