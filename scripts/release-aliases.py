"""Print the shared image tags this exact version is eligible to advance."""
import re
import sys
from pathlib import Path

version = tuple(map(int, sys.argv[1].split(".")))
versions = {
    tuple(map(int, tag.split(".")))
    for tag in Path(sys.argv[2]).read_text().splitlines()
    if re.fullmatch(r"(?:0|[1-9][0-9]*)(?:\.(?:0|[1-9][0-9]*)){2}", tag)
}
if version not in versions:
    sys.exit("The published version is missing from the registry listing")
for length in (0, 1, 2):
    prefix = version[:length]
    if version == max(v for v in versions if v[:length] == prefix):
        print(".".join(map(str, prefix)) or "latest")
