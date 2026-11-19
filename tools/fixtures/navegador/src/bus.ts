import mitt from 'mitt';

export interface Linea {
  producto: string;
  cantidad: number;
}

export interface Pedido {
  id: string;
  lineas?: Linea[];
}

export const bus = mitt<{ pedido: Pedido }>();
