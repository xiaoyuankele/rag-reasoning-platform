"""PDF 自动化测试共用的最小夹具生成工具。"""

from __future__ import annotations

from pathlib import Path

from pypdf import PdfWriter
from pypdf.generic import DecodedStreamObject, DictionaryObject, NameObject


def write_text_pdf(path: Path, page_texts: list[str]) -> None:
    """生成页数和 ASCII 文字都可控的最小测试 PDF。

    Args:
        path: 测试 PDF 的输出路径。
        page_texts: 每个元素对应一个物理页面；空字符串生成空白文字页。

    Raises:
        UnicodeEncodeError: 测试文字不是 ASCII。当前夹具使用 PDF 内置
            Helvetica 字体，只服务于解析链路测试，不验证中文字体嵌入。

    Notes:
        本函数只生成自动化测试夹具，不属于生产 PDF 创建能力。
    """

    writer = PdfWriter()
    font = DictionaryObject(
        {
            NameObject("/Type"): NameObject("/Font"),
            NameObject("/Subtype"): NameObject("/Type1"),
            NameObject("/BaseFont"): NameObject("/Helvetica"),
        }
    )
    font_reference = writer._add_object(font)

    for text in page_texts:
        page = writer.add_blank_page(width=612, height=792)
        page[NameObject("/Resources")] = DictionaryObject(
            {
                NameObject("/Font"): DictionaryObject(
                    {NameObject("/F1"): font_reference}
                )
            }
        )

        escaped_text = (
            text.replace("\\", "\\\\")
            .replace("(", "\\(")
            .replace(")", "\\)")
        )
        content = DecodedStreamObject()
        content.set_data(
            f"BT /F1 12 Tf 72 720 Td ({escaped_text}) Tj ET".encode("ascii")
        )
        page[NameObject("/Contents")] = writer._add_object(content)

    with path.open("wb") as output:
        writer.write(output)
