#!/usr/bin/env python3
"""从主控 MCP tools/list 生成或核验 README 工具清单。"""

from __future__ import annotations

import argparse
import difflib
import json
import os
from pathlib import Path
import re
import sys
from typing import Any
from urllib.request import Request, urlopen


MARKER_START = "<!-- MMWX-MCP-TOOLS:START -->"
MARKER_END = "<!-- MMWX-MCP-TOOLS:END -->"
TOOL_NAME_RE = re.compile(r"^[a-z][a-z0-9_]*$")

# 分类顺序是 README 的展示约定；没有匹配到的新前缀会进入“其它”，不会被静默丢掉。
CATEGORIES = (
    ("自定义规则", ("custom_rule_",)),
    ("节点", ("node_",)),
    ("套餐", ("package_",)),
    ("旧版模板", ("rule_template_",)),
    ("远程服务器", ("server_",)),
    ("测速端", ("speedtest_",)),
    ("订阅", ("subscribe_file_", "temp_subscription_")),
    ("V3 模板", ("template_v3_",)),
    ("流量与隧道", ("traffic_", "tunnel_")),
    ("用户", ("user_",)),
    ("Xray 辅助", ("xray_",)),
)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument("--input", type=Path, help="已保存的 tools/list JSON 响应")
    source.add_argument("--url", help="主控 MCP 地址，例如 https://example.com/mcp")
    parser.add_argument("--version", required=True, help="主控发布版本，例如 v0.4.9")
    parser.add_argument(
        "--readme",
        type=Path,
        default=Path(__file__).resolve().parents[1] / "README.md",
        help="要核验或更新的 README 路径",
    )
    action = parser.add_mutually_exclusive_group()
    action.add_argument("--check", action="store_true", help="仅核验 README，不写文件")
    action.add_argument("--write", action="store_true", help="更新 README 中的生成区块")
    return parser.parse_args()


def decode_response(raw: bytes) -> dict[str, Any]:
    text = raw.decode("utf-8")
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        # 部分 MCP 服务会返回 SSE；只取包含 JSON-RPC 响应的 data 行。
        for line in text.splitlines():
            if not line.startswith("data:"):
                continue
            try:
                payload = json.loads(line.removeprefix("data:").strip())
            except json.JSONDecodeError:
                continue
            if isinstance(payload, dict) and ("result" in payload or "error" in payload):
                return payload
        raise ValueError("响应既不是 JSON，也没有可解析的 SSE data 行")


def fetch_registry(url: str) -> dict[str, Any]:
    token = os.environ.get("MMWX_API_TOKEN", "").strip()
    headers = {
        "Content-Type": "application/json",
        "Accept": "application/json, text/event-stream",
    }
    if token:
        # 令牌只从环境变量读取，避免进入命令历史或进程参数列表。
        headers["Authorization"] = f"Bearer {token}"
    body = json.dumps(
        {"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {}},
        separators=(",", ":"),
    ).encode("utf-8")
    request = Request(url, data=body, headers=headers, method="POST")
    with urlopen(request, timeout=30) as response:
        return decode_response(response.read())


def load_registry(args: argparse.Namespace) -> dict[str, Any]:
    if args.input is not None:
        return decode_response(args.input.read_bytes())
    return fetch_registry(args.url)


def extract_tools(payload: dict[str, Any]) -> list[dict[str, Any]]:
    if "error" in payload:
        raise ValueError(f"MCP 返回错误: {payload['error']}")
    tools = payload.get("result", {}).get("tools")
    if not isinstance(tools, list):
        raise ValueError("响应缺少 result.tools 数组")

    seen: set[str] = set()
    normalized: list[dict[str, Any]] = []
    for tool in tools:
        if not isinstance(tool, dict):
            raise ValueError("result.tools 含非对象成员")
        name = tool.get("name")
        if not isinstance(name, str) or not TOOL_NAME_RE.fullmatch(name):
            raise ValueError(f"工具名格式无效: {name!r}")
        if name in seen:
            raise ValueError(f"注册表含重复工具名: {name}")
        seen.add(name)
        normalized.append(tool)
    return normalized


def requires_confirm(tool: dict[str, Any]) -> bool:
    properties = tool.get("inputSchema", {}).get("properties", {})
    return isinstance(properties, dict) and "confirm" in properties


def category_for(name: str) -> str:
    for label, prefixes in CATEGORIES:
        if name.startswith(prefixes):
            return label
    return "其它"


def render_block(version: str, tools: list[dict[str, Any]]) -> str:
    grouped: dict[str, list[dict[str, Any]]] = {label: [] for label, _ in CATEGORIES}
    grouped["其它"] = []
    for tool in tools:
        grouped[category_for(tool["name"])].append(tool)

    lines = [
        MARKER_START,
        f"以官方主控 `{version}` 的运行时 `tools/list` 实测结果为准，共 {len(tools)} 个工具；",
        f"对应客户端日志计数应为 `registered {len(tools)} tool(s)`：",
        "",
    ]
    for label, _ in CATEGORIES:
        members = sorted(grouped[label], key=lambda item: item["name"])
        if not members:
            continue
        rendered = " ".join(
            f"`{tool['name']}`" + ("*" if requires_confirm(tool) else "")
            for tool in members
        )
        lines.append(f"- {label}：{rendered}")
    if grouped["其它"]:
        rendered = " ".join(
            f"`{tool['name']}`" + ("*" if requires_confirm(tool) else "")
            for tool in sorted(grouped["其它"], key=lambda item: item["name"])
        )
        lines.append(f"- 其它：{rendered}")
    lines.extend(["", MARKER_END])
    return "\n".join(lines)


def replace_block(readme: str, generated: str) -> str:
    pattern = re.compile(
        re.escape(MARKER_START) + r".*?" + re.escape(MARKER_END),
        re.DOTALL,
    )
    if not pattern.search(readme):
        raise ValueError("README 缺少工具清单生成区块标记")
    return pattern.sub(generated, readme, count=1)


def main() -> int:
    args = parse_args()
    try:
        tools = extract_tools(load_registry(args))
        generated = render_block(args.version, tools)
        if not args.check and not args.write:
            print(generated)
            return 0

        current = args.readme.read_text(encoding="utf-8")
        expected = replace_block(current, generated)
        if args.write:
            args.readme.write_text(expected, encoding="utf-8")
            print(f"已更新 {args.readme}，工具数: {len(tools)}")
            return 0
        if current == expected:
            print(f"PASS: {args.readme} 与 {args.version} 注册表一致，工具数: {len(tools)}")
            return 0

        diff = difflib.unified_diff(
            current.splitlines(),
            expected.splitlines(),
            fromfile=str(args.readme),
            tofile=f"{args.readme}（注册表生成）",
            lineterm="",
        )
        print("\n".join(diff), file=sys.stderr)
        return 1
    except (OSError, ValueError) as error:
        print(f"错误: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
