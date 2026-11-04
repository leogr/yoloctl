import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

namespace = {"__name__": "installer_test"}
exec(Path(__file__).with_name("install.py").read_text(), namespace)


class Response(io.BytesIO):
    def geturl(self):
        return "https://downloads.example/cli/release.tar.gz"


class InstallTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.binary = Path(self.temp.name) / "bin" / "agy"
        self.binary.parent.mkdir()
        self.binary.write_bytes(b"previous executable")
        self.metadata = Path(str(self.binary) + ".json")
        self.metadata.write_text('{"version":"1.0.0"}')
        self.spec = {"binary": str(self.binary), "member": "antigravity",
                     "manifest_base": "https://manifest.example/linux_",
                     "artifact_base": "https://downloads.example/cli/"}

    def run_install(self, corrupt=False, symlink=False, outside=False):
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode="w:gz") as tar:
            member = tarfile.TarInfo("antigravity")
            payload = b"new executable (must never run during installation)"
            if symlink:
                member.type = tarfile.SYMTYPE
                member.linkname = "/etc/shadow"
            else:
                member.size = len(payload)
            tar.addfile(member, io.BytesIO(payload))
        archive = buffer.getvalue()
        manifest = {"version": "1.2.17", "sha512": hashlib.sha512(archive).hexdigest(),
                    "url": "https://downloads.example/cli/release.tar.gz"}
        if corrupt:
            manifest["sha512"] = "0" * 128
        if outside:
            manifest["url"] = "https://elsewhere.example/release.tar.gz"

        def urlopen(url, **kwargs):
            if url == "https://manifest.example/linux_amd64.json":
                return Response(json.dumps(manifest).encode())
            self.assertEqual(url, manifest["url"])
            return Response(archive)

        with patch("urllib.request.urlopen", urlopen), patch("platform.machine", return_value="x86_64"), patch("platform.system", return_value="Linux"):
            namespace["install"](self.spec)

    def test_replaces_binary_and_metadata_without_executing(self):
        self.run_install()
        self.assertTrue(self.binary.read_bytes().startswith(b"new executable"))
        self.assertEqual(self.binary.stat().st_mode & 0o777, 0o755)
        self.assertEqual(json.loads(self.metadata.read_text())["version"], "1.2.17")
        self.assertEqual(list(self.binary.parent.glob(".install-*")), [])

    def test_rejected_downloads_preserve_existing_installation(self):
        for option in ("corrupt", "symlink", "outside"):
            with self.subTest(option=option), self.assertRaises(ValueError):
                self.run_install(**{option: True})
            self.assertEqual(self.binary.read_bytes(), b"previous executable")
            self.assertEqual(json.loads(self.metadata.read_text())["version"], "1.0.0")
            self.assertEqual(list(self.binary.parent.glob(".install-*")), [])


unittest.main()
