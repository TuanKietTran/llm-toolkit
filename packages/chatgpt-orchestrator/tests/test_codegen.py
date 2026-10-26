"""Unit tests for the pure codegen helpers (prompt building + code extraction from a reply)."""

from __future__ import annotations

from orchestrator.workers.codegen import build_code_prompt, extract_code


def test_build_code_prompt_names_language_and_demands_only_a_fence():
    p = build_code_prompt("a function that adds two numbers", "python")
    assert "in python" in p
    assert "```python" in p
    assert "ONLY a single fenced code block" in p
    assert "a function that adds two numbers" in p


def test_build_code_prompt_without_language():
    p = build_code_prompt("reverse a string")
    assert "```" in p
    assert " in " not in p.split("Request:")[0].replace("the block", "")  # no "in <lang>"


def test_extract_single_fenced_block():
    reply = "Here you go:\n\n```python\ndef f():\n    return 1\n```\n\nHope that helps!"
    r = extract_code(reply, "python")
    assert r.had_fence is True
    assert r.code == "def f():\n    return 1"
    assert r.language == "python"


def test_extract_prefers_block_matching_requested_language():
    reply = (
        "```bash\necho hi\n```\n"
        "```python\nprint('hi')\n```\n"
    )
    r = extract_code(reply, "python")
    assert r.code == "print('hi')"
    assert r.language == "python"


def test_extract_falls_back_to_longest_block_when_no_language_match():
    reply = "```js\nx\n```\n```js\nconst a = 1;\nconst b = 2;\n```\n"
    r = extract_code(reply, "python")  # requested language absent
    assert r.code == "const a = 1;\nconst b = 2;"
    assert r.language == "js"


def test_extract_untagged_fence():
    reply = "```\nplain code\n```"
    r = extract_code(reply)
    assert r.had_fence is True
    assert r.code == "plain code"
    assert r.language is None


def test_extract_cpp_language_tag():
    reply = "```c++\nint main(){return 0;}\n```"
    r = extract_code(reply, "c++")
    assert r.had_fence is True
    assert r.language == "c++"


def test_extract_no_fence_returns_whole_reply_flagged():
    reply = "def f():\n    return 2\n"
    r = extract_code(reply, "python")
    assert r.had_fence is False
    assert r.code == "def f():\n    return 2"
    assert r.language == "python"


def test_extract_empty_reply():
    r = extract_code("", "python")
    assert r.code == ""
    assert r.had_fence is False
