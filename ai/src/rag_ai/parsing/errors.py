"""文档解析阶段使用的稳定内部错误。"""

from __future__ import annotations


# Retryability belongs to the error category, not to an individual parser.
# Keeping the policy in one table prevents PDF and DOCX implementations from
# returning contradictory retry decisions for the same stable error code.
ERROR_RETRYABILITY: dict[str, bool] = {
    "unsupported_format": False,
    "source_not_found": False,
    "source_access_denied": False,
    "password_required": False,
    "extraction_not_permitted": False,
    "ocr_required": False,
    "invalid_content": False,
    "resource_limit_exceeded": False,
    "parse_failed": False,
    "internal_error": True,
}


def retryable_for(code: str) -> bool:
    """查询稳定错误码是否适合由系统自动重试。

    Args:
        code: Go/Python v1 契约中约定的稳定文档处理错误码。

    Returns:
        ``True`` 表示可以有限自动重试，``False`` 表示需要用户或环境先修复。

    Raises:
        ValueError: ``code`` 不是已登记的稳定错误码。
    """

    if code not in ERROR_RETRYABILITY:
        raise ValueError(f"unknown document processing error code: {code!r}")

    return ERROR_RETRYABILITY[code]


class DocumentProcessingError(Exception):
    """与 JSON 传输格式无关、可以预期的文档处理失败。

    Attributes:
        code: 供程序稳定判断的错误码。
        message: 可以跨到 Go 后端日志的安全错误说明。
        retryable: 当前错误是否适合自动重试。
    """

    def __init__(self, code: str, message: str) -> None:
        """根据稳定错误码和安全消息创建文档处理异常。"""

        message = message.strip()
        if not message:
            raise ValueError("document processing error message must not be blank")

        super().__init__(message)
        self.code = code
        self.message = message
        self.retryable = retryable_for(code)
