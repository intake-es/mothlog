import { vi } from 'vitest';

// La compilación JIT del primer componente de cada fichero cuenta dentro del test.
vi.setConfig({ testTimeout: 20_000 });
