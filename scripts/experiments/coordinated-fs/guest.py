#!/usr/bin/env python3
"""Synthetic guest bootstrap; requires the deliberately isolated trial VM."""
import ctypes
import importlib.util
import os
from pathlib import Path
import struct
import sys

libc = ctypes.CDLL(None, use_errno=True)
libc.mount.argtypes = [ctypes.c_char_p, ctypes.c_char_p, ctypes.c_char_p,
                       ctypes.c_ulong, ctypes.c_char_p]
if libc.mount(b"account", b"/account", b"virtiofs", 0, b"dax=always"):
    raise OSError(ctypes.get_errno(), "cannot mount synthetic DAX account")

spec = importlib.util.spec_from_file_location("contract", "/probe/storage-contract.py")
contract = importlib.util.module_from_spec(spec)
spec.loader.exec_module(contract)


class VerifiedWorker(contract.Worker):
    def dispatch(self, command):
        result = super().dispatch(command)
        if command["op"] == "open":
            descriptor = self.handles[command["handle"]]
            data = ctypes.create_string_buffer(256)
            if libc.statx(descriptor, b"", 0x1000, 0x7FF, data):  # AT_EMPTY_PATH
                raise OSError(ctypes.get_errno(), "statx failed")
            attributes = struct.unpack_from("=Q", data.raw, 8)[0]
            supported = struct.unpack_from("=Q", data.raw, 56)[0]
            if not attributes & supported & 0x200000:  # STATX_ATTR_DAX
                raise RuntimeError("regular file is not actually using DAX")
            print("YCF_DAX " + command["name"], file=sys.stderr, flush=True)
        if command["op"] == "info":
            result["prototype"] = "coordinated-fs-private-abi-v1"
            result["mountinfo"] = Path("/proc/self/mountinfo").read_text()
        return result


contract.Worker = VerifiedWorker
sys.exit(contract.worker_main("/account", sys.argv[1]))
