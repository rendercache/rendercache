# SPDX-License-Identifier: Apache-2.0
"""Deterministic binary archive; run through build-release.sh."""
import gzip
import hashlib
import io
import pathlib
import sys
import tarfile

output = pathlib.Path(sys.argv[1])
version = sys.argv[2]
archive = output / f"rendercache_{version}_linux_amd64.tar.gz"
with archive.open("wb") as raw:
    with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0) as compressed:
        with tarfile.open(fileobj=compressed, mode="w") as tar:
            for source, name in [(output / "rendercache", "rendercache"),
                                 (pathlib.Path("LICENSE"), "LICENSE"),
                                 (pathlib.Path("THIRD_PARTY_NOTICES"), "THIRD_PARTY_NOTICES"),
                                 (pathlib.Path("README.md"), "README.md"),
                                 (pathlib.Path("rendercache.example.json"), "rendercache.example.json"),
                                 (pathlib.Path("docs/deployment.md"), "docs/deployment.md")]:
                data = source.read_bytes()
                item = tarfile.TarInfo(name)
                item.size = len(data)
                item.mode = 0o755 if name == "rendercache" else 0o644
                item.mtime = item.uid = item.gid = 0
                tar.addfile(item, io.BytesIO(data))
checksum = hashlib.sha256(archive.read_bytes()).hexdigest()
(output / "SHA256SUMS").write_text(f"{checksum}  {archive.name}\n")
print(archive)
