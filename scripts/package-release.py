# SPDX-License-Identifier: Apache-2.0
"""Deterministic binary archive; run through build-release.sh.

Writes rendercache_<os>_<arch>.tar.gz (Linux, macOS) or .zip (Windows) and a
SHA256SUMS line for it. Names carry no version so latest-download links last.
"""
import gzip
import hashlib
import io
import pathlib
import sys
import tarfile
import zipfile

output = pathlib.Path(sys.argv[1])
goos, goarch, binary = sys.argv[2], sys.argv[3], sys.argv[4]
files = [(output / binary, binary),
         (pathlib.Path("LICENSE"), "LICENSE"),
         (pathlib.Path("NOTICE"), "NOTICE"),
         (pathlib.Path("THIRD_PARTY_LICENSES.md"), "THIRD_PARTY_LICENSES.md"),
         (pathlib.Path("README.md"), "README.md"),
         (pathlib.Path("rendercache.example.json"), "rendercache.example.json"),
         (pathlib.Path("docs/deployment.md"), "docs/deployment.md")]
stem = f"rendercache_{goos}_{goarch}"
if goos == "windows":
    archive = output / f"{stem}.zip"
    with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED) as z:
        for source, name in files:
            info = zipfile.ZipInfo(name, date_time=(1980, 1, 1, 0, 0, 0))
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = 0o644 << 16
            z.writestr(info, source.read_bytes())
else:
    archive = output / f"{stem}.tar.gz"
    with archive.open("wb") as raw:
        with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0) as compressed:
            with tarfile.open(fileobj=compressed, mode="w") as tar:
                for source, name in files:
                    data = source.read_bytes()
                    item = tarfile.TarInfo(name)
                    item.size = len(data)
                    item.mode = 0o755 if name == binary else 0o644
                    item.mtime = item.uid = item.gid = 0
                    tar.addfile(item, io.BytesIO(data))
checksum = hashlib.sha256(archive.read_bytes()).hexdigest()
with (output / "SHA256SUMS").open("a", newline="\n") as sums:
    sums.write(f"{checksum}  {archive.name}\n")
print(archive)
