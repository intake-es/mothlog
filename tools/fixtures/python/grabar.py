import logging
import random
import secrets
import sys
import time
import uuid

import sentry_sdk
from fastapi.testclient import TestClient
from sentry_sdk.crons import monitor

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
elif caso == "cron-ok":

    @monitor(
        monitor_slug="limpieza-nocturna",
        monitor_config={
            "schedule": {"type": "crontab", "value": "0 3 * * *"},
            "timezone": "Europe/Madrid",
            "checkin_margin": 5,
            "max_runtime": 20,
        },
    )
    def limpiar():
        time.sleep(0.2)

    limpiar()
elif caso == "cron-error":

    @monitor(
        monitor_slug="enviar-facturas",
        monitor_config={"schedule": {"type": "interval", "value": 2, "unit": "hour"}},
    )
    def enviar_facturas():
        raise TimeoutError("el banco no responde")

    try:
        enviar_facturas()
    except TimeoutError:
        pass
else:
    sys.exit(f"caso desconocido: {caso}")
sentry_sdk.flush(10)
