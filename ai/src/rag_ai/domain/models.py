"""文档处理流程内部使用的稳定领域模型。"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path


@dataclass(frozen=True)
class DocumentSource:
    """应用层处理一份文档时真正需要的可信来源信息。

    Attributes:
        source_path: Go 文件存储层解析出的可信绝对路径。
        mime_type: Go 已确认并用于选择处理器的 MIME 类型。
    """

    source_path: Path
    mime_type: str


@dataclass(frozen=True)
class ProcessingLimits:
    """单次文档处理必须遵守的资源与分块限制。

    Attributes:
        max_chunk_characters: 单个文本块允许的最大 Unicode 字符数。
        max_file_bytes: 当前格式处理器允许读取的最大文件字节数。
        max_pages: 当前格式处理器允许读取的最大物理页数。
    """

    max_chunk_characters: int
    max_file_bytes: int
    max_pages: int


@dataclass(frozen=True)
class PageText:
    """从一个有固定页码的文档页面提取出的原始文字。

    Attributes:
        page_number: 从 1 开始的物理页码。
        text: 页面提取文字；空白页使用空字符串，不在提取层删除。

    Notes:
        本模型不引用 pypdf 或 LangChain，因此应用层可以在不认识具体解析库
        的情况下编排页面规范化和分块。
    """

    page_number: int
    text: str


@dataclass(frozen=True)
class ExtractedDocument:
    """格式提取器完成一次源文件读取后返回的统一中间结果。

    Attributes:
        pages: 按物理页顺序排列的原始页面文字。
        detected_title: 从源文件元数据中尽力识别的可选标题。

    Notes:
        本模型只保存 Application 后续真正需要的数据，不保留文件句柄、
        ``PdfReader`` 或具体解析库对象。因此 Infrastructure 可以在返回前
        安全关闭源文件，Application 也不会依赖 pypdf。
    """

    pages: list[PageText]
    detected_title: str | None = None


@dataclass(frozen=True)
class TextChunk:
    """应用层生成、尚未转换为传输协议的一条统一文本块。

    Attributes:
        index: 从 0 开始连续递增的全局文本块序号。
        content: 已规范化且非空的正文。
        page_start: 可选的起始物理页码。
        page_end: 可选的结束物理页码。
    """

    index: int
    content: str
    page_start: int | None = None
    page_end: int | None = None


@dataclass(frozen=True)
class ProcessingResult:
    """应用层完成一份文档处理后返回的框架无关结果。

    Attributes:
        chunks: 按原文顺序排列的统一文本块。
        detected_title: 处理器自动识别的可选文献标题；缺失时为 ``None``。
    """

    chunks: list[TextChunk]
    detected_title: str | None = None
