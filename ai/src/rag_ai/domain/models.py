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
class ExtractionMetrics:
    """文档提取器能够准确测量的内部阶段指标。

    Attributes:
        source_open_ms: 文件预检、打开并构造解析器所用毫秒数。
        metadata_read_ms: 尽力读取可选元数据所用毫秒数。
        text_extract_ms: 遍历全部页面并提取文字所用毫秒数。
        page_count: 解析器观察到的物理页数。
        slowest_page_number: 文字提取最慢页面的 1-based 页码。
        slowest_page_ms: 最慢页面提取文字所用毫秒数。

    Notes:
        所有耗时都使用单调高精度时钟测量。小于 1ms 的已执行阶段会记为
        0ms；未执行阶段不应伪造本对象。
    """

    source_open_ms: int
    metadata_read_ms: int
    text_extract_ms: int
    page_count: int
    slowest_page_number: int
    slowest_page_ms: int


@dataclass(frozen=True)
class ExtractedDocument:
    """格式提取器完成一次源文件读取后返回的统一中间结果。

    Attributes:
        pages: 按物理页顺序排列的原始页面文字。
        detected_title: 从源文件元数据中尽力识别的可选标题。
        metrics: 提取器内部阶段的可选观测数据；测试 Fake 或旧实现可不提供。

    Notes:
        本模型只保存 Application 后续真正需要的数据，不保留文件句柄、
        ``PdfReader`` 或具体解析库对象。因此 Infrastructure 可以在返回前
        安全关闭源文件，Application 也不会依赖 pypdf。
    """

    pages: list[PageText]
    detected_title: str | None = None
    metrics: ExtractionMetrics | None = None


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
class ProcessingMetrics:
    """一次 Python 文档处理成功后产生的分阶段观测数据。

    ``python_total_ms`` 覆盖提取、页面规范化和分块；其余字段用于定位
    内部主要耗时。字段使用整数毫秒，避免跨语言传输浮点计时噪声。
    """

    python_total_ms: int
    source_open_ms: int
    metadata_read_ms: int
    text_extract_ms: int
    text_split_ms: int
    page_count: int
    slowest_page_number: int
    slowest_page_ms: int


@dataclass(frozen=True)
class ProcessingResult:
    """应用层完成一份文档处理后返回的框架无关结果。

    Attributes:
        chunks: 按原文顺序排列的统一文本块。
        detected_title: 处理器自动识别的可选文献标题；缺失时为 ``None``。
        metrics: Python 内部阶段的可选观测数据。
    """

    chunks: list[TextChunk]
    detected_title: str | None = None
    metrics: ProcessingMetrics | None = None
