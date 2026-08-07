from __future__ import annotations

from pathlib import Path
import sys
import unittest


AI_ROOT = Path(__file__).resolve().parents[1]
SOURCE_ROOT = AI_ROOT / "src"
sys.path.insert(0, str(SOURCE_ROOT))

from rag_ai.parsing.errors import (  # noqa: E402
    DocumentProcessingError,
    ERROR_RETRYABILITY,
    retryable_for,
)


class ProcessingErrorTests(unittest.TestCase):
    def test_retryable_for_returns_policy_for_every_stable_code(self) -> None:
        for code, expected in ERROR_RETRYABILITY.items():
            with self.subTest(code=code):
                self.assertEqual(retryable_for(code), expected)

    def test_retryable_for_rejects_unknown_code(self) -> None:
        with self.assertRaises(ValueError):
            retryable_for("unknown_error")

    def test_document_processing_error_preserves_safe_fields(self) -> None:
        error = DocumentProcessingError(
            "password_required",
            "PDF requires a password",
        )

        self.assertEqual(error.code, "password_required")
        self.assertEqual(error.message, "PDF requires a password")
        self.assertFalse(error.retryable)
        self.assertEqual(str(error), "PDF requires a password")


if __name__ == "__main__":
    unittest.main()
