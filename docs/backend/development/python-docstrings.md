# Python Docstring 与 IDE 悬停说明规范

## 目的

本项目使用中文 docstring 帮助初学者在 IDE 中悬停查看类、函数和方法的调用说明。普通 `#` 注释只解释实现内部的关键原因，不能替代 docstring。

## 基本规则

- `ai/src` 中新增的模块、类、函数和方法必须有中文 docstring；
- 公共调用点必须说明用途、参数、返回值和可能抛出的异常；
- dataclass 应使用 `Attributes` 解释字段含义和取值约束；
- 私有辅助函数至少写一句职责说明；
- 测试函数可以由明确的 `test_...` 名称表达场景，不重复写无信息量 docstring；
- 测试夹具和辅助方法仍需说明输入、输出以及它只用于测试还是可用于生产；
- docstring 描述契约和责任边界，不逐行翻译实现代码。

## 函数模板

```python
def example(source_path: Path, *, max_pages: int) -> list[str]:
    """读取受控来源并返回按页排列的文字。

    Args:
        source_path: 后端解析出的可信绝对路径。
        max_pages: 当前任务允许处理的最大页数。

    Returns:
        按物理页顺序排列的文字列表。

    Raises:
        DocumentProcessingError: 文件不可读或解析失败。
    """
```

## 类和 dataclass 模板

```python
@dataclass(frozen=True)
class PageText:
    """一页文档的提取结果。

    Attributes:
        page_number: 从 1 开始的物理页码。
        text: 页面提取文字；空白页使用空字符串。
    """

    page_number: int
    text: str
```

## 注释与 docstring 的区别

```python
def process() -> None:
    """告诉调用者这个函数能做什么。"""

    # 告诉维护者这里为什么必须采用这种实现。
```

IDE 悬停主要显示 docstring。行内注释通常只在打开源码时可见，因此面向调用者的重要信息必须写入 docstring。
