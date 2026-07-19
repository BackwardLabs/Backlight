from __future__ import annotations

import argparse
import asyncio
import importlib.util
import json
import os
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace


SCRIPT_PATH = Path(__file__).with_name("pre_lumos_agent.py")
SPEC = importlib.util.spec_from_file_location("pre_lumos_agent", SCRIPT_PATH)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError(f"could not load {SCRIPT_PATH}")
PRE_LUMOS_AGENT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(PRE_LUMOS_AGENT)


class CollectCaseArtifactsTest(unittest.TestCase):
    def test_collects_canonical_report_bundle_paths(self) -> None:
        with tempfile.TemporaryDirectory() as temp_dir:
            output_root = Path(temp_dir)
            artifacts = {
                "helios_signal_context.json": '{"protocol":"Demo"}\n',
                "report_bundle/README.md": "# Demo incident\n",
                "report_bundle/report/REPORT.md": "# Report\n",
                "report_bundle/report/RCA.md": "# Root cause\n",
                "report_bundle/report/run_summary.json": '{"status":"pass"}\n',
                "report_bundle/poc/PoC.t.sol": "contract PoC {}\n",
            }
            for relative_path, content in artifacts.items():
                path = output_root / relative_path
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(content, encoding="utf-8")

            ignored = output_root / "report_bundle" / "artifacts" / "secret.txt"
            ignored.parent.mkdir(parents=True, exist_ok=True)
            ignored.write_text("ignore me\n", encoding="utf-8")

            materials = PRE_LUMOS_AGENT.collect_case_artifacts(output_root, 256 * 1024)

            labels = {str(Path(material["label"]).relative_to(output_root)) for material in materials}
            self.assertEqual(labels, set(artifacts))
            self.assertTrue(all(material["kind"] == "case-artifact" for material in materials))

    def test_discards_legacy_openai_transport_environment(self) -> None:
        keys = ("OPENAI_API_KEY", "OPENAI_BASE_URL", "BACKLIGHT_PRE_LUMOS_OPENAI_BASE_URL")
        original = {key: os.environ.get(key) for key in keys}
        try:
            for key in keys:
                os.environ[key] = "legacy-value"
            PRE_LUMOS_AGENT.discard_legacy_openai_transport_env()
            self.assertTrue(all(key not in os.environ for key in keys))
        finally:
            for key, value in original.items():
                if value is None:
                    os.environ.pop(key, None)
                else:
                    os.environ[key] = value


class CodexSDKRunnerTest(unittest.TestCase):
    def test_runs_orchestrator_with_codex_sdk_and_no_openai_proxy(self) -> None:
        row = {field: None for field in PRE_LUMOS_AGENT.REQUIRED_FIELDS}
        row.update(
            {
                "name": "Demo Protocol",
                "slug": "demo-protocol",
                "hackedAt": "2026-07-19",
                "chains": ["Ethereum"],
                "amount": 100,
                "category": "Exploit",
            }
        )
        response = f"```json\n{json.dumps([row])}\n```"

        class FakeThread:
            async def run(self, prompt: str, **kwargs: object) -> SimpleNamespace:
                self.prompt = prompt
                self.kwargs = kwargs
                return SimpleNamespace(error=None, final_response=response)

        class FakeAsyncCodex:
            last_config: object = None

            def __init__(self, config: object) -> None:
                type(self).last_config = config

            async def __aenter__(self) -> "FakeAsyncCodex":
                return self

            async def __aexit__(self, *args: object) -> None:
                return None

            async def thread_start(self, **kwargs: object) -> FakeThread:
                self.thread_kwargs = kwargs
                return FakeThread()

        sdk = SimpleNamespace(
            ApprovalMode=SimpleNamespace(deny_all="deny-all"),
            AsyncCodex=FakeAsyncCodex,
            CodexConfig=lambda **kwargs: kwargs,
            Sandbox=SimpleNamespace(read_only="read-only"),
        )

        with tempfile.TemporaryDirectory() as temp_dir:
            args = argparse.Namespace(
                case_output_root=[temp_dir],
                dry_run=True,
                max_skill_bytes=512 * 1024,
                model=None,
                note=[],
                seed_root=temp_dir,
                skill_dir=str(PRE_LUMOS_AGENT.DEFAULT_SKILL_DIR),
                skip_agent_validators=True,
                web_search=False,
                year="2026",
            )
            _, rows, reports = asyncio.run(PRE_LUMOS_AGENT.run_agent(args, [], sdk=sdk))

        self.assertEqual([item["slug"] for item in rows], ["demo-protocol"])
        self.assertEqual(reports, [])
        self.assertTrue(FakeAsyncCodex.last_config["codex_bin"])
        self.assertEqual(FakeAsyncCodex.last_config["config_overrides"], ('web_search="disabled"',))


if __name__ == "__main__":
    unittest.main()
