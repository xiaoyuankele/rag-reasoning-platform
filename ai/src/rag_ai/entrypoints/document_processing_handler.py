"""把 v1 传输契约转换为框架无关的文档处理用例调用。"""

from __future__ import annotations

from typing import Any

from rag_ai.application.ports import DocumentProcessingUseCase
from rag_ai.contracts.document_processing_v1 import (
    ProcessingChunk,
    ProcessingRequest,
    success_response,
)
from rag_ai.domain.models import DocumentSource, ProcessingLimits


def process_request(
    request: ProcessingRequest,
    service: DocumentProcessingUseCase,
) -> dict[str, Any]:
    """处理一条已校验的 v1 请求并构造成功响应。

    本函数位于传输契约和应用层之间，只负责两次边界转换：

    1. 把契约层的 ``ProcessingRequest`` 缩减为应用层真正需要的
       ``DocumentSource`` 和 ``ProcessingLimits``；
    2. 把应用层返回的领域 ``TextChunk`` 转换为 v1 ``ProcessingChunk``。

    Args:
        request: 已由契约层完成字段、类型和范围校验的 v1 请求。
        service: 已经注入统一文档提取器和文本切分器的应用服务。

    Returns:
        可以直接序列化到标准输出的 v1 成功响应字典。

    Raises:
        DocumentProcessingError: 应用服务返回的稳定文档处理失败。
        ContractError: 应用结果无法满足 v1 输出契约。
    """

    source = DocumentSource(
        source_path=request.document.source_path,
        mime_type=request.document.mime_type,
    )
    limits = ProcessingLimits(
        max_chunk_characters=request.options.max_chunk_characters,
        max_file_bytes=request.options.max_pdf_file_bytes,
        max_pages=request.options.max_pdf_pages,
    )

    result = service.process(source, limits)
    contract_chunks = [
        ProcessingChunk(
            index=chunk.index,
            content=chunk.content,
            page_start=chunk.page_start,
            page_end=chunk.page_end,
        )
        for chunk in result.chunks
    ]

    return success_response(
        request.request_id,
        contract_chunks,
        detected_title=result.detected_title,
    )
