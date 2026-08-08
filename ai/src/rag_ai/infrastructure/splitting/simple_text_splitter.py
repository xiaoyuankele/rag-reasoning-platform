"""不依赖第三方框架的轻量文本切分器。"""

from __future__ import annotations


class SimpleTextSplitter:
    """按照空格和字符边界执行轻量文本分块。"""

    def split(
        self,
        text: str,
        *,
        max_chunk_characters: int,
    ) -> list[str]:
        """把一段文本切成不超过指定字符数的非空文本块。

        切分器优先在字符上限范围内寻找最后一个空格，避免从单词中间
        切开；范围内没有空格时，则按照字符上限直接切分。

        Args:
            text: 已经由应用层规范化、准备分块的文本。
            max_chunk_characters: 单个文本块允许包含的最大字符数。

        Returns:
            按原文顺序排列的字符串列表；空白文本返回空列表。

        Raises:
            TypeError: ``text`` 不是字符串。
            ValueError: ``max_chunk_characters`` 不是正整数，或者是布尔值。
        """

        if not isinstance(text, str):
            raise TypeError("text must be a string")

        # Python 的 bool 是 int 的子类，因此必须先显式拒绝 bool；
        # 否则 True 会被错误地当成数字 1。
        if (
            isinstance(max_chunk_characters, bool)
            or not isinstance(max_chunk_characters, int)
            or max_chunk_characters <= 0
        ):
            raise ValueError(
                "max_chunk_characters must be a positive integer"
            )

        remaining = text.strip()
        chunks: list[str] = []

        while len(remaining) > max_chunk_characters:
            # end 参数不包含在搜索范围内，所以使用上限 + 1，允许在
            # max_chunk_characters 所在位置的空格前完成切分。
            split_at = remaining.rfind(
                " ",
                0,
                max_chunk_characters + 1,
            )
            if split_at <= 0:
                split_at = max_chunk_characters

            chunks.append(remaining[:split_at].rstrip())
            remaining = remaining[split_at:].lstrip()

        if remaining:
            chunks.append(remaining)

        return chunks
