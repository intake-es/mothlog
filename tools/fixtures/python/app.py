import logging
import os
import sysconfig
from pathlib import Path

import sentry_sdk
import structlog
from fastapi import FastAPI
from pydantic import BaseModel

HERE = Path(__file__).resolve().parent
# Las rutas de la máquina que graba no van en los fixtures.
PATHS = sorted(
    {
        sysconfig.get_paths()["purelib"]: "/srv/api/.venv/lib/python3.14/site-packages",
        sysconfig.get_paths()["stdlib"]: "/usr/lib/python3.14",
        str(HERE): "/srv/api",
    }.items(),
    key=lambda kv: -len(kv[0]),
)


class NotFound(Exception):
    pass


def scrub(value):
    if isinstance(value, dict):
        return {k: scrub(v) for k, v in value.items()}
    if isinstance(value, list):
        return [scrub(v) for v in value]
    if isinstance(value, str):
        for local, generic in PATHS:
            value = value.replace(local, generic)
    return value


def before_send(event, hint):
    exc = hint.get("exc_info")
    if exc and isinstance(exc[1], NotFound):
        return None
    return scrub(event)


sentry_sdk.init(
    dsn=os.environ["SENTRY_DSN"],
    release=os.environ.get("SENTRY_RELEASE", "fixture-release-1"),
    environment=os.environ.get("SENTRY_ENVIRONMENT", "production"),
    server_name="api-1",
    before_send=before_send,
)
sentry_sdk.set_tag("component", "api")

logging.basicConfig(level=logging.INFO, format="%(name)s %(message)s")
structlog.configure(
    processors=[structlog.stdlib.render_to_log_kwargs],
    logger_factory=structlog.stdlib.LoggerFactory(),
    wrapper_class=structlog.stdlib.BoundLogger,
)
log = structlog.get_logger("tienda.stock")

app = FastAPI()


class Pedido(BaseModel):
    cliente: str
    cantidad: int


@app.get("/pedidos/{pedido_id}/media")
async def media(pedido_id: str):
    lineas = {"unidades": 12, "envios": 0}
    return {"pedido": pedido_id, "media": lineas["unidades"] / lineas["envios"]}


@app.get("/pedidos/importar-ok")
async def importar_ok():
    return Pedido.model_validate({"cliente": "c_1042", "cantidad": 12})


@app.get("/pedidos/importar")
async def importar():
    return Pedido.model_validate({"cliente": "c_1042", "cantidad": "doce"})


@app.get("/pedidos/{pedido_id}")
async def pedido(pedido_id: str):
    raise NotFound(pedido_id)


def sincronizar_stock(almacen: str) -> int:
    existencias = {"norte": 4, "sur": 9}
    try:
        return existencias[almacen]
    except KeyError:
        log.exception("stock.sincronizacion_fallida", almacen=almacen)
        return 0


def sincronizar_stock_estricto(almacen: str) -> int:
    existencias = {"norte": 4, "sur": 9}
    return existencias[almacen]


def reembolsar(pedido_id: str) -> None:
    log.error("pagos.reembolso_rechazado", pedido_id=pedido_id, status=502)
