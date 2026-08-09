"""SimpleTextSplitter 的单元测试。"""

import unittest

from rag_ai.infrastructure.splitting.simple_text_splitter import (
    SimpleTextSplitter,
)


class SimpleTextSplitterTests(unittest.TestCase):
    """验证轻量文本切分器的正常结果和输入边界。"""

    def test_split_returns_expected_chunks(self) -> None:
        """不同文本应该生成对应的字符串块。"""

        splitter = SimpleTextSplitter()
        cases = [
            (
                "prefer space boundary",
                "one two three",
                7,
                ["one two", "three"],
            ),
            (
                "hard split without spaces",
                "abcdefghij",
                4,
                ["abcd", "efgh", "ij"],
            ),
            (
                "split Chinese characters",
                "你好世界测试",
                4,
                ["你好世界", "测试"],
            ),
            (
                "blank text",
                "  ",
                4,
                [],
            ),
        ]

        for name, input_text, max_characters, expected in cases:
            # subTest 为循环中的当前案例添加标签；某个案例失败时，
            # unittest 会报告它的 name，并继续验证其他案例。
            with self.subTest(name=name):
                actual = splitter.split(
                    input_text,
                    max_chunk_characters=max_characters,
                )

                self.assertEqual(actual, expected)

    def test_split_rejects_invalid_max_chunk_characters(self) -> None:
        """分块上限必须是正整数，并且不能使用布尔值。"""

        splitter = SimpleTextSplitter()
        invalid_limits = [0, True]

        for invalid_limit in invalid_limits:
            with self.subTest(invalid_limit=invalid_limit):
                # 内层 with 声明：下面这次调用必须抛出 ValueError。
                with self.assertRaises(ValueError):
                    splitter.split(
                        "hello",
                        max_chunk_characters=invalid_limit,
                    )

    def test_split_rejects_non_string_text(self) -> None:
        """非字符串内容必须被明确拒绝。"""

        splitter = SimpleTextSplitter()
        invalid_texts = [123, [1, 2, 3]]

        for invalid_text in invalid_texts:
            with self.subTest(invalid_text=invalid_text):
                with self.assertRaises(TypeError):
                    # 测试故意传入非法类型；仍需提供合法的字符上限，
                    # 才能确保 TypeError 真正来自 text 类型校验。
                    splitter.split(
                        invalid_text,  # type: ignore[arg-type]
                        max_chunk_characters=4,
                    )


if __name__ == "__main__":
    unittest.main()
