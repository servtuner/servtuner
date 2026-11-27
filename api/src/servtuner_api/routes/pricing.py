"""The install's price book: what new runs are priced with, and the token-price
datasheet the operator supplies."""

from fastapi import APIRouter, HTTPException, Request, Response, status

from servtuner_api.price_book import PriceBook, PriceBookResource
from servtuner_api.routes.common import Service, json_body_schema, target_errors

router = APIRouter()


@router.get("/price-book", response_model_exclude_none=True)
def get_price_book(service: Service) -> PriceBookResource:
    """The price book, with the analysis.infra_pricing and token_pricing blocks new runs get."""
    return service.price_book.get()


@router.put(
    "/price-book", response_model_exclude_none=True, openapi_extra=json_body_schema(PriceBook)
)
async def save_price_book(request: Request, service: Service) -> PriceBookResource:
    """Replace the price book. Runs already made keep the prices they carry."""
    body = await request.body()
    with target_errors():
        return service.price_book.save(body)


@router.get("/price-book/datasheet", response_class=Response)
def get_datasheet(service: Service) -> Response:
    """The uploaded datasheet, byte for byte; 404 without one."""
    content = service.price_book.datasheet_content()
    if content is None:
        raise HTTPException(status.HTTP_404_NOT_FOUND, "the price book has no uploaded datasheet")
    return Response(content=content, media_type="application/json")


@router.put(
    "/price-book/datasheet",
    response_model_exclude_none=True,
    openapi_extra={
        "requestBody": {
            "required": True,
            "description": "LiteLLM's model-price JSON, as downloaded",
            "content": {"application/json": {"schema": {"type": "object"}}},
        }
    },
)
async def upload_datasheet(request: Request, service: Service) -> PriceBookResource:
    """Upload the token-price datasheet (LiteLLM's model-price JSON): runs priced by the datasheet
    fetch it from the api, so an install with no internet access can price by the token."""
    body = await request.body()
    with target_errors():
        return service.price_book.save_datasheet(body)


@router.delete("/price-book/datasheet", status_code=status.HTTP_204_NO_CONTENT)
def delete_datasheet(service: Service) -> Response:
    if not service.price_book.delete_datasheet():
        raise HTTPException(status.HTTP_404_NOT_FOUND, "the price book has no uploaded datasheet")
    return Response(status_code=status.HTTP_204_NO_CONTENT)
