"""工具注册表必须完整，输入契约变化不能被名称相同掩盖。"""
import json
from pathlib import Path
import re
import tempfile
import unittest
from unittest.mock import patch
import sync_mcp_tools as sync


def tool(name, schema=None):
    return {"name": name, "inputSchema": schema or {"type": "object", "properties": {}}}


class RegistryTests(unittest.TestCase):
    def response(self, payload):
        from io import BytesIO
        return BytesIO(json.dumps(payload).encode())

    def test_pagination_collects_all_tools(self):
        calls = []
        def serve(request, timeout):
            data = json.loads(request.data)
            calls.append(data["params"])
            if len(calls) == 1:
                return self.response({"result": {"tools": [tool("node_list")], "nextCursor": "page2"}})
            return self.response({"result": {"tools": [tool("forward_status")]}})
        with patch.object(sync, "urlopen", serve):
            tools = sync.extract_tools(sync.fetch_registry("https://example.invalid/mcp"))
        self.assertEqual([t["name"] for t in tools], ["node_list", "forward_status"])
        self.assertEqual(calls, [{}, {"cursor": "page2"}])

    def test_incomplete_offline_input_and_repeating_cursor_rejected(self):
        page = {"result": {"tools": [tool("node_list")], "nextCursor": "again"}}
        with self.assertRaises(ValueError):
            sync.extract_tools(page)
        with patch.object(sync, "urlopen", lambda *a, **k: self.response(page)):
            with self.assertRaises(ValueError):
                sync.fetch_registry("https://example.invalid/mcp")

    def test_duplicate_across_pages_rejected(self):
        responses = [self.response({"result": {"tools": [tool("node_list")], "nextCursor": "2"}}),
                     self.response({"result": {"tools": [tool("node_list")]}})]
        with patch.object(sync, "urlopen", side_effect=responses):
            with self.assertRaises(ValueError):
                sync.fetch_registry("https://example.invalid/mcp")

    def test_schema_changes_detect_required_type_enum_and_confirm(self):
        base = tool("node_list")
        old = sync.render_contract("v1", [base])
        for schema in [
            {"required": ["scope"]},
            {"properties": {"scope": {"type": "integer"}}},
            {"properties": {"scope": {"enum": ["all", "mine"]}}},
            {"properties": {"confirm": {"type": "boolean"}}},
        ]:
            self.assertNotEqual(old, sync.render_contract("v1", [tool("node_list", schema)]))

    def test_schema_omits_annotations_but_preserves_parameter_names(self):
        schema = {"type": "object", "description": "private-description", "properties": {
            "default": {"type": "string", "default": "private-default"},
            "description": {"type": "string", "examples": ["private-example"]},
        }}
        output = sync.render_contract("v1", [tool("node_list", schema)])
        self.assertNotIn("private-", output)
        parsed = json.loads(output)["tools"][0]["inputSchema"]["properties"]
        self.assertEqual(set(parsed), {"default", "description"})

    def test_failed_write_preserves_existing_readme(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            source, readme = root / "input.json", root / "README.md"
            source.write_text(json.dumps({"result": {"tools": [tool("node_list")], "nextCursor": "2"}}))
            readme.write_text("keep-existing")
            with patch("sys.argv", ["sync", "--input", str(source), "--readme", str(readme), "--version", "v1", "--write"]):
                self.assertEqual(sync.main(), 2)
            self.assertEqual(readme.read_text(), "keep-existing")

    def test_skills_tool_references_have_capability_declarations(self):
        root = Path(__file__).resolve().parents[1]
        declared = json.loads((root / "tool-capabilities.json").read_text())
        readme = (root / "README.md").read_text()
        block = readme.split(sync.MARKER_START)[1].split(sync.MARKER_END)[0]
        known = set(re.findall(r"`([a-z][a-z0-9_]+)`", block))
        optional = set(declared["optional_tools"])
        used = set()
        for skill in root.glob("mmwx-*/SKILL.md"):
            content = skill.read_text()
            self.assertIn("完整 `tools/list`", content)
            used |= set(re.findall(r"`((?:node|server|package|user|forward|cert|logs|task|traffic|tunnel|speedtest|subscribe_file|temp_subscription|template_v3|custom_rule|xray)_[a-z0-9_]+)`", content))
        # 这些是参数/状态字段，不能因为前缀相同被误认为新工具。
        used -= {"server_id", "node_id", "traffic_mode", "xray_running"}
        self.assertFalse(used - known - optional, f"未声明的工具: {used - known - optional}")
        self.assertTrue(optional <= used)
        allowlist = set(re.findall(r"^        - ([a-z][a-z0-9_]+)$", readme, re.M))
        self.assertTrue(used <= allowlist, f"Hermes 清单缺少: {used - allowlist}")


if __name__ == "__main__":
    unittest.main()
