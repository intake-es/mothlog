import logging
import random
import secrets
import sys
import uuid

import sentry_sdk
from fastapi.testclient import TestClient

from app import app, reembolsar, sincronizar_stock

client = TestClient(app, raise_server_exceptions=False)
caso = sys.argv[1]
if caso == "excepcion":
    client.get("/pedidos/p_1042/media")
elif caso == "validacion":
    client.get("/pedidos/importar")
elif caso == "descartado":
    client.get("/pedidos/p_9999")
elif caso == "log-exception":
    sincronizar_stock("oeste")
elif caso == "log-error":
    reembolsar("p_1042")
elif caso == "tarea":
    logging.getLogger("worker").error(
        f"task failed id={uuid.uuid4().hex} name='enviar_factura' worker={secrets.token_hex(4)} "
        f"try=4 duration_ms={random.randint(1000, 300000)} error=TimeoutError('timed out')"
    )
else:
    sys.exit(f"caso desconocido: {caso}")
sentry_sdk.flush(10)
